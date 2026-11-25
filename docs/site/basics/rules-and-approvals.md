# Rules and approvals

Rules decide what happens when an AI client calls a tool: **allow** it,
**ask** a person to approve it first, or **deny** it. When a rule asks, the
call waits until an approver decides in the browser.

Approvals in the browser arrived in 0.3.0, the **Rules** page in 0.5.0, and
grants, withdrawing and **History** in 0.13.0.

## When no rule matches

At the top of **Rules**, **When no rule matches** sets the default for every
call no rule covers: **allow**, **ask for approval** or **deny**. Choose one
and **Save**. A new Privo allows.

## Add a rule

Under **Add a rule**:

1. **Server**: one server, or **every server**.
2. **Tool**: the tool's name as the server calls it (`merge_pull_request`,
   not `github__merge_pull_request`), `*` for all of them, or a pattern such
   as `delete_*`.
3. **Who**: **everyone**, or one role: members, approvers, auditors, admins or
   owners.
4. **Action**: **ask for approval**, **deny** or **allow**.
5. **Note**, if you like: why the rule exists.
6. Choose **Add rule**.

When several rules match a call, the most specific one wins. Some rules to
start from:

| Server | Tool | Who | Action | What it does |
| --- | --- | --- | --- | --- |
| `github` | `merge_pull_request` | everyone | ask for approval | A person agrees before any merge |
| every server | `delete_*` | members | deny | No member's AI client deletes anything |
| `stripe` | `*` | everyone | ask for approval | Every Stripe call waits for a person |

To remove a rule, choose **Delete** next to it in the list.

## Approve or deny a call

When a rule says ask, the call appears on **Approvals → Waiting**, for every
owner, admin and approver. Each request shows the tool and server, who asked
and with which token, when the request expires, and the call's arguments.

1. Read the arguments: they are exactly what the AI client wants to send.
2. Choose how far your yes goes, under **Allow**: **only this call**, or **this
   tool for 15 minutes**, **1 hour** or **8 hours**.
3. Choose **Approve**, or **Deny**.

The AI client waits up to `PRIVO_APPROVAL_HOLD` for the decision (see the
[configuration reference](../installation/configuration.md)). Decided in
time, the call goes ahead at once. Otherwise the client is told that the
request waits for a person, with a link, and to make the same call again
once it is approved.

**Nobody approves their own calls.** A request you made yourself waits for
someone else.

### Grants

Approving "this tool for" a while creates a grant: a rule, shown on **Rules**
with its end time, that allows the same tool for that person until then. A
grant only ever turns an "ask" into "allow": it never outranks a deny.

### History

**Approvals → History** lists the requests that were decided: when each was
asked, the tool, who asked, and how it ended.

## Withdraw your own request

When a call of yours waits, **Start** shows it under **Waiting for approval**.
Choose **Withdraw** if you no longer want it. If your AI client calls again,
it asks again.

## Going further

- [Rules on arguments](../using/rules.md#rules-on-arguments): ask or deny
  only when a call's arguments match, such as `repo` equals `prod`.
- [Rules as a file](../using/rules.md#rules-as-a-file): export every rule as
  YAML, review it like code, and import it back.
- [Rules for one team or one person](../using/rules.md), which the rules file
  and the admin API can name.
