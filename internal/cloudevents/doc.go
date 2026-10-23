// Package cloudevents owns CloudEvents v1.0 envelope encoding plus the
// panel-locked extension set (traceparent, midnightrequestid,
// midnighttenantid).
//
// Panel decision pinning (line 177 / WEBHOOK_AND_EVENT_CONTRACT.md §3):
//
//	"CloudEvents binding pinned. Required CE extensions: traceparent,
//	 midnightrequestid, midnighttenantid. The HTTP delivery uses the
//	 structured content mode (Content-Type: application/cloudevents+json)."
//
// Lane 4 ships the encoding/decoding primitives the inbound webhook
// receiver and the eventual outbound dispatcher both consume. Strict
// validation of `specversion`, `id`, `source`, `type`, the required
// extensions, and the `dataschema` URL convention is panel-locked.
package cloudevents
