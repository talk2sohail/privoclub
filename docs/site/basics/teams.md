# Organising with teams

Teams decide which servers each person reaches. A person sees the servers of
every team they are in, and nothing else: a call to any other server's tools
is answered as if the tool did not exist.

Teams arrived in 0.6.0
([ADR 0020](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0020-teams-and-server-catalog.md)).

## Everyone

**Everyone** is built in, and every member is in it. A server given to
**Everyone** is for the whole organisation. Servers added before teams
existed, and servers added without choosing a team, are for everyone.

## Create a team

1. Open **Teams**. Under **New team**, choose what to **Start from**: a blank
   team, or a profile:
    - **Developers**: code, issues, errors and deployments;
    - **Designers**: design files, whiteboards and sites;
    - **Product managers**: docs, roadmaps, tasks and analytics;
    - **Sales and support**: customers, conversations and the CRM.
2. Give it a **Name**, and a **Description** if you like.
3. Choose **Create team**.

A team made from a profile suggests the catalog's servers for that kind of
team, each one click away.

## Add people

Open the team (**Open** next to it in the list). Under **People**, pick a
person and choose **Add to team**. To take someone out, choose **×** next
to their name. People must have joined Privo first: invite them
on **People**.

## Give a team servers

In the open team, under **Servers**, pick a server and choose **Give
access**; **×** next to a server takes it away from the team. You can also
choose teams from the server's side: **Who can use
it** on the server's page, or when you add it.

A person in several teams reaches the servers of all of them.

!!! note
    A server that no team reaches is reachable by nobody. Before you take a
    server away from **Everyone**, give it to the teams that need it.

## Delete a team

In the open team, type its short name to confirm and choose **Delete team**.
This also deletes the rules that name the team. Its people keep their other
teams.

## Going further

- [Per-team toolsets](../advanced/toolsets.md): a team can see only some of a
  server's tools.
- [Rules for a team](../advanced/policy-engine.md#who-a-rule-is-for): allow,
  ask or deny for one team.
- [Teams through the admin API](../advanced/api-and-automation.md#teams).
