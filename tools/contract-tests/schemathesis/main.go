// Command schemathesis runs deterministic, in-process state-machine contract
// checks. The directory name is retained for discoverability; the harness is
// pure Go and requires neither Python nor a live Midnight server.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const prefix = "/api/v1alpha"

func main() {
	seeds := flag.Int("seeds", 64, "number of deterministic state-machine seeds")
	steps := flag.Int("steps", 48, "random actions per seed")
	flag.Parse()
	if *seeds <= 0 || *steps <= 0 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "seeds and steps must be positive; no positional arguments are accepted")
		os.Exit(64)
	}
	if err := Run(os.Stdout, *seeds, *steps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Run executes deterministic state-machine scenarios without network access.
func Run(output io.Writer, seeds, steps int) error {
	if seeds <= 0 || steps <= 0 {
		return errors.New("stateful contract: seeds and steps must be positive")
	}
	for seed := 0; seed < seeds; seed++ {
		machine := newMachine(int64(seed))
		if err := machine.run(steps); err != nil {
			return fmt.Errorf("stateful contract seed %d: %w", seed, err)
		}
	}
	_, err := fmt.Fprintf(output, "stateful API contract: ok (%d seeds, %d steps each)\n", seeds, steps)
	return err
}

type machine struct {
	ctx      context.Context
	seed     int64
	random   *rand.Rand
	store    *api.MemoryOperationStore
	handler  http.Handler
	key      string
	body     []byte
	conflict []byte
	op       orailtypes.Operation
	accepted struct {
		status      int
		body        []byte
		contentType string
		location    string
	}
	events int
}

func newMachine(seed int64) *machine {
	store := api.NewMemoryOperationStore()
	return &machine{
		ctx:      context.Background(),
		seed:     seed,
		random:   rand.New(rand.NewSource(seed)), //nolint:gosec -- deterministic property exploration, not security.
		store:    store,
		handler:  api.NewRouterWithOperationStore(store),
		key:      fmt.Sprintf("stateful-operation-%d", seed),
		body:     []byte(fmt.Sprintf(`{"job":"stateful-%d"}`, seed)),
		conflict: []byte(fmt.Sprintf(`{"job":"conflict-%d"}`, seed)),
	}
}

func (machine *machine) run(steps int) error {
	if err := machine.create(); err != nil {
		return err
	}
	if err := machine.replay(); err != nil {
		return err
	}
	if err := machine.conflictingReplay(); err != nil {
		return err
	}
	for step := 0; step < steps; step++ {
		var err error
		switch machine.random.Intn(6) {
		case 0:
			err = machine.get()
		case 1:
			err = machine.replay()
		case 2:
			err = machine.conflictingReplay()
		case 3:
			if machine.seed%3 == 0 || machine.op.State == orailtypes.OperationQueued {
				err = machine.startOrSucceed()
			} else {
				err = machine.get()
			}
		case 4:
			if machine.seed%3 == 1 {
				err = machine.fail()
			} else {
				err = machine.get()
			}
		case 5:
			if machine.seed%3 == 2 {
				err = machine.cancel()
			} else {
				err = machine.conflictingReplay()
			}
		}
		if err != nil {
			return fmt.Errorf("step %d: %w", step, err)
		}
		if err := machine.assertInvariants(); err != nil {
			return fmt.Errorf("step %d invariant: %w", step, err)
		}
	}

	// Ensure the bounded seed set covers every terminal state even when the
	// random prefix happened to stay read-only.
	if !terminal(machine.op.State) {
		if machine.op.State == orailtypes.OperationQueued && machine.seed%2 == 0 {
			if err := machine.start(); err != nil {
				return err
			}
		}
		switch machine.seed % 3 {
		case 0:
			if err := machine.succeed(); err != nil {
				return err
			}
		case 1:
			if err := machine.fail(); err != nil {
				return err
			}
		case 2:
			if err := machine.cancel(); err != nil {
				return err
			}
		}
	}
	if err := machine.assertTerminalRejection(); err != nil {
		return err
	}
	return machine.assertInvariants()
}

func (machine *machine) create() error {
	response := machine.request(http.MethodPost, prefix+"/_demo/long", machine.body, machine.key)
	if response.Code != http.StatusAccepted {
		return fmt.Errorf("create status = %d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &machine.op); err != nil {
		return fmt.Errorf("decode create: %w", err)
	}
	if machine.op.ID == "" || machine.op.State != orailtypes.OperationQueued || machine.op.CreatedAt.IsZero() || machine.op.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid create operation %#v", machine.op)
	}
	if response.Header().Get("Location") != prefix+"/operations/"+string(machine.op.ID) {
		return errors.New("create Location does not identify operation")
	}
	machine.accepted.status = response.Code
	machine.accepted.body = append([]byte(nil), response.Body.Bytes()...)
	machine.accepted.contentType = response.Header().Get("Content-Type")
	machine.accepted.location = response.Header().Get("Location")
	machine.events = 1
	return machine.assertInvariants()
}

func (machine *machine) replay() error {
	before := machine.events
	response := machine.request(http.MethodPost, prefix+"/_demo/long", machine.body, machine.key)
	if response.Code != http.StatusAccepted || response.Header().Get("Idempotent-Replayed") != "true" {
		return fmt.Errorf("exact replay status/header = %d/%q body=%s", response.Code, response.Header().Get("Idempotent-Replayed"), response.Body.String())
	}
	if response.Code != machine.accepted.status ||
		!bytes.Equal(response.Body.Bytes(), machine.accepted.body) ||
		response.Header().Get("Content-Type") != machine.accepted.contentType ||
		response.Header().Get("Location") != machine.accepted.location {
		return fmt.Errorf(
			"exact replay transport = %d/%q/%q/%s, want original %d/%q/%q/%s",
			response.Code,
			response.Header().Get("Content-Type"),
			response.Header().Get("Location"),
			response.Body.String(),
			machine.accepted.status,
			machine.accepted.contentType,
			machine.accepted.location,
			machine.accepted.body,
		)
	}
	if machine.events != before {
		return errors.New("exact replay changed modeled event count")
	}
	// Idempotency replay is an immutable historical representation. Assert the
	// independently evolving operation lifecycle through its resource endpoint.
	return machine.get()
}

func (machine *machine) conflictingReplay() error {
	before := machine.op
	response := machine.request(http.MethodPost, prefix+"/_demo/long", machine.conflict, machine.key)
	if response.Code != http.StatusConflict || problemCode(response.Body.Bytes()) != string(failurecodes.IdempotencyKeyReusedDifferentBody) {
		return fmt.Errorf("conflicting replay = %d/%q body=%s", response.Code, problemCode(response.Body.Bytes()), response.Body.String())
	}
	after, err := machine.store.Get(machine.ctx, machine.op.ID)
	if err != nil {
		return err
	}
	if after != before {
		return errors.New("conflicting replay mutated operation")
	}
	return nil
}

func (machine *machine) get() error {
	response := machine.request(http.MethodGet, prefix+"/operations/"+string(machine.op.ID), nil, "")
	if response.Code != http.StatusOK {
		return fmt.Errorf("get status = %d body=%s", response.Code, response.Body.String())
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		return err
	}
	if operation != machine.op {
		return fmt.Errorf("get operation = %#v, want %#v", operation, machine.op)
	}
	return nil
}

func (machine *machine) startOrSucceed() error {
	switch machine.op.State {
	case orailtypes.OperationQueued:
		return machine.start()
	case orailtypes.OperationRunning:
		return machine.succeed()
	default:
		_, err := machine.store.Transition(machine.ctx, machine.op.ID, orailtypes.OperationRunning)
		if !errors.Is(err, api.ErrOperationInvalidTransition) {
			return fmt.Errorf("terminal direct transition error = %v", err)
		}
		return nil
	}
}

func (machine *machine) start() error {
	return machine.directTransition(orailtypes.OperationRunning, "running")
}

func (machine *machine) succeed() error {
	if machine.op.State == orailtypes.OperationQueued {
		if err := machine.start(); err != nil {
			return err
		}
	}
	return machine.directTransition(orailtypes.OperationSucceeded, "succeeded")
}

func (machine *machine) directTransition(state orailtypes.OperationState, message string) error {
	operation, err := machine.store.Transition(machine.ctx, machine.op.ID, state)
	if err != nil {
		return err
	}
	if _, err := machine.store.AppendEvent(machine.ctx, api.OperationEvent{OperationID: operation.ID, State: state, Message: message}); err != nil {
		return err
	}
	machine.op = operation
	machine.events++
	return nil
}

func (machine *machine) fail() error {
	wasTerminal := terminal(machine.op.State)
	response := machine.request(http.MethodPost, prefix+"/operations/"+string(machine.op.ID)+"/fail", []byte(`{"code":"WORKER_ERROR","message":"worker failed"}`), "")
	if wasTerminal {
		return machine.assertInvalidTransition(response)
	}
	if response.Code != http.StatusAccepted {
		return fmt.Errorf("fail status = %d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &machine.op); err != nil {
		return err
	}
	if machine.op.State != orailtypes.OperationFailed || machine.op.Code != "WORKER_ERROR" || machine.op.Reason != "worker failed" {
		return fmt.Errorf("fail operation = %#v", machine.op)
	}
	machine.events++
	return nil
}

func (machine *machine) cancel() error {
	wasTerminal := terminal(machine.op.State)
	response := machine.request(http.MethodPost, prefix+"/operations/"+string(machine.op.ID)+"/cancel", nil, "")
	if wasTerminal {
		return machine.assertInvalidTransition(response)
	}
	if response.Code != http.StatusAccepted {
		return fmt.Errorf("cancel status = %d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &machine.op); err != nil {
		return err
	}
	if machine.op.State != orailtypes.OperationCanceled {
		return fmt.Errorf("cancel operation = %#v", machine.op)
	}
	machine.events++
	return nil
}

func (machine *machine) assertTerminalRejection() error {
	if !terminal(machine.op.State) {
		return fmt.Errorf("expected terminal state, got %s", machine.op.State)
	}
	before := machine.op
	beforeEvents := machine.events
	if err := machine.fail(); err != nil {
		return err
	}
	if err := machine.cancel(); err != nil {
		return err
	}
	if machine.op != before || machine.events != beforeEvents {
		return errors.New("rejected terminal transitions changed model")
	}
	return nil
}

func (machine *machine) assertInvalidTransition(response *httptest.ResponseRecorder) error {
	if response.Code != http.StatusConflict || problemCode(response.Body.Bytes()) != string(failurecodes.OperationInvalidTransition) {
		return fmt.Errorf("terminal transition = %d/%q body=%s", response.Code, problemCode(response.Body.Bytes()), response.Body.String())
	}
	return nil
}

func (machine *machine) assertInvariants() error {
	operations, err := machine.store.List(machine.ctx, api.OperationListFilter{})
	if err != nil {
		return err
	}
	if len(operations) != 1 || operations[0] != machine.op {
		return fmt.Errorf("stored operations = %#v, want exactly %#v", operations, machine.op)
	}
	if machine.op.CreatedAt.IsZero() || machine.op.UpdatedAt.IsZero() || machine.op.UpdatedAt.Before(machine.op.CreatedAt) {
		return fmt.Errorf("invalid lifecycle timestamps: %#v", machine.op)
	}
	events, err := machine.store.ListEvents(machine.ctx, machine.op.ID)
	if err != nil {
		return err
	}
	if len(events) != machine.events {
		return fmt.Errorf("event count = %d, want %d", len(events), machine.events)
	}
	for index, event := range events {
		if event.Seq != index+1 || event.OperationID != machine.op.ID || event.CreatedAt.IsZero() {
			return fmt.Errorf("invalid event %d: %#v", index, event)
		}
	}
	if len(events) == 0 || events[len(events)-1].State != machine.op.State {
		return fmt.Errorf("latest event state does not match operation: %#v", events)
	}
	return nil
}

func (machine *machine) request(method, target string, body []byte, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	machine.handler.ServeHTTP(response, request)
	return response
}

func problemCode(body []byte) string {
	var problem struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &problem) != nil {
		return ""
	}
	if problem.Code != "" {
		return problem.Code
	}
	return problem.Error.Code
}

func terminal(state orailtypes.OperationState) bool {
	switch state {
	case orailtypes.OperationSucceeded, orailtypes.OperationFailed, orailtypes.OperationCanceled:
		return true
	default:
		return false
	}
}
