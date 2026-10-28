# Security review of access rules (F09) and approvals (F10), 2026-10-28

An independent reviewer read the change and proved three findings with
failing tests. All findings are fixed. Each fix has a test unless noted.

| Severity | Finding | Fix | Test |
| --- | --- | --- | --- |
| High (critical with one owner) | An AI agent with an owner or admin token could use it on the admin API to approve its own calls (mint an approver token, enable self-approval, or add an allow rule) | Gateway and control tokens; approvers get no gateway token (ADR 0014) | `TestGatewayTokensCannotAdministerOrApprove`, `TestTokenKindsAreSeparated` |
| High | Self-approval with a second token: the check compared token id or name, not the person | Tokens carry a person; approvals compare persons | `TestSelfApproval`, `TestApprovalIsBoundToArgumentsAndSingleUse` |
| High | A rule on `*` did not cover tool names containing `/` | Own glob matcher where `*` spans any characters | `TestStarCoversSlashes`, `TestGlob` |
| High | Default deny and server-wide deny rules did not apply to prompts and resources | Rules evaluated for prompts and resources; "ask" there fails closed | `TestDefaultDenyCoversPromptsAndResources` |
| Medium | Two identical concurrent calls created two approvals; the approved one was never used | Unique index on open approvals; insert-or-reuse; approved and denied rows preferred | `TestConcurrentIdenticalCallsShareOneApproval` |
| Medium | Reformatting the same arguments (spacing, key order) dodged a denial and spammed approvers; duplicate keys could show one value and run another | Arguments frozen to one canonical form, which is hashed, approved and sent; duplicate keys refused | `TestCanonical*`, `FuzzCanonical`, `TestDenialSurvivesReformattedArguments` |
| Medium | Unbounded approvals; held calls polled the single database writer | Polling uses the read pool; 20 pending approvals and 5 waiting calls per requester | `TestPendingCapPerRequester` |
| Medium | Preset rules stayed in force after a tool's definition changed | Preset rules are tied to the reviewed definition's hash | `TestPresetRuleFollowsReviewedDefinition` |
| Medium | Approval arguments were kept forever and shown in history | Shown only while pending or approved; deleted once used or denied; every view is audited | `TestArgumentsPurgedOnceUsedOrDenied` |
| Low | An approval could be used up when the call then failed to start | Approval restored if the audit intent cannot be written | `TestRestoreAfterFailedStart` |
| Low | Refusal messages told the model which rule matched | Generic message to the model; details in the audit log | Covered by acceptance assertions |
| Low | The approver was missing from the call's audit row | `approved_by` recorded on the call | `TestApprovalIsBoundToArgumentsAndSingleUse` |
| Low | Missing annotations are not treated as destructive, unlike the MCP default | Kept on purpose (annotations are untrusted) and documented; organisations wanting caution set the default to "ask" | n/a |
| Low | Rules re-read on every call | Tracked: cache per org with invalidation | Not yet |

Also fixed: rules for a deleted server are removed with it, so a reused alias
does not inherit them.

Still open: a withdraw endpoint for requesters; tests for the control package
itself; caching rules per organisation.
