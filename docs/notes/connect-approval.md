# First-connection approval

A new agent's `phone_connect` returns `approval_pending` with an `approval_id`. Repeating that call with the same `device`, `task`, `apps` and `read_only` keeps waiting on the same card. The phone is leased only after the person allows it. A different device, task, app list or `read_only` is `invalid_argument` and leaves the card open. A denied, timed-out, withdrawn, used or unknown id does nothing and does not open another card. Call without `approval_id` to ask again.

`Binding.Device` stays empty, so the card is not tied to one phone. The requested device string is part of the approval key. The key length-prefixes every field, so a NUL or U+001F inside one of those strings cannot make two different requests match. An omitted device is still resolved when the approval is consumed, not when the card opens.

```sh
go test ./internal/mcp/ ./internal/gateway/ -count=1 -timeout 180s -run 'TestPhoneConnectApprovalContinuation|TestAgentApprovalContinuationBinding|TestAgentApprovalDenyTimeoutAndCancelDoNothing|TestAgentConnectKeyDoesNotMergeDifferentRequests|TestAgentApprovalControlCharactersDoNotContinue|TestNewAgentNeedsApproval|TestToolList'
```
