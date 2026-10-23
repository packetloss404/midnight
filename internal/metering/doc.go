// Package metering records normalized usage events and aggregates them into
// deterministic billing windows. It is side-effect free so callers can plug in
// durable storage later without changing the accounting rules.
package metering
