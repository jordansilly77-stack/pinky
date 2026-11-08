# First-connection approval

A new agent's `phone_connect` returns `approval_pending` with an `approval_id`. Repeating that call with the same `device`, `task`, `apps` and `read_only` keeps waiting on the same card. The phone is leased only after the person allows it. A different device, task, app list or `read_only` is `invalid_argument` and leaves the card open. A denied, timed-out, withdrawn, used or unknown id does nothing and does not open another card. Call without `approval_id` to ask again.

`Binding.Device` stays empty, so the card is not tied to one phone. The requested device is part of the approval key.

```sh
go test ./internal/mcp/ ./internal/gateway/ -count=1 -timeout 180s -run 'TestPhoneConnectApprovalContinuation|TestAgentApprovalContinuationBinding|TestAgentApprovalDenyTimeoutAndCancelDoNothing|TestNewAgentNeedsApproval|TestToolList'
```
