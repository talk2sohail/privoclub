# Inviting and managing people

People join Privo by invitation and sign in with their GitHub account. On
**People**, owners and admins invite them, give them roles, and remove or
restore them.

Invites and removal arrived in 0.5.0, and restoring a member in 0.17.0.

## Invite someone

1. Open **People**. Under **Invite someone**, enter the **Person**: the name
   their tokens and approvals will show, such as `ada`.
2. Choose their **Roles**: **member** for someone who uses tools through an AI
   client. See the roles below.
3. Choose **Create invite link**.
4. Send the link to them. It works once and expires after 72 hours, and it is
   shown only now.

They open the link and choose **Continue with GitHub**. Privo ties their
GitHub account to them, and they land on **Start**, where they make a token
for their AI client.

## Roles

| Role | What it is for |
| --- | --- |
| **member** | Uses tools through an AI client |
| **approver** | Decides other people's calls that wait for approval |
| **auditor** | Reads the activity and the audit trail |
| **admin** | Manages servers, teams, rules and people, and can do the rest |
| **owner** | Like an admin, and can make other admins and owners |

Most people are members. Give **admin** only to the people who run Privo.

To change someone's roles, choose **change** next to their roles on
**People**, pick the new ones and choose **Save roles**. The change applies to
their next request. Nobody changes their own roles.

## Remove someone

Choose **Remove…** next to the person, then **Remove** with their name to
confirm. Removing someone at once:

- signs them out;
- revokes every token with their name (they stop working within seconds);
- deletes their connected accounts, and asks each provider to revoke them.

Their history stays in the audit trail.

## Restore someone

A removed person stays in the list. Choose **Restore** with their name to
bring them back with their GitHub account and their roles. They sign in
again and make new tokens: their old tokens and connections do not come
back.

## Going further

- [Roles](../using/roles.md): holding several roles, and who may change whose.
- Limiting sign-in to members of your GitHub organisations:
  `PRIVO_GITHUB_ORG_IDS` in the
  [configuration reference](../installation/configuration.md).
- [Inviting from the command line](../using/people.md).
