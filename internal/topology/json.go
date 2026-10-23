// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package topology

import (
	"encoding/json"
	"fmt"
)

func marshalNodes(nodes []Node) ([]byte, error) {
	if nodes == nil {
		nodes = []Node{}
	}
	value, err := json.Marshal(nodes)
	if err != nil {
		return nil, fmt.Errorf("marshal topology nodes: %w", err)
	}
	return value, nil
}

func unmarshalNodes(value []byte, nodes *[]Node) error {
	if err := json.Unmarshal(value, nodes); err != nil {
		return fmt.Errorf("decode topology nodes: %w", err)
	}
	return Validate(*nodes)
}
