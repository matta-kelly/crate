---
type: moc
description: "Root of crate: the tool that makes a repo's workspace from its crate.toml, and keeps each repo's main on the server through a merge queue. Read for what crate is made of and where each part is described, before work on crate or on how a repo is checked, landed or run."
status: active
---
crate: the tool that makes a repo's workspace, the running system of one change, from the repo's `crate.toml`, and keeps each repo's main on the server, through a merge queue. A person, a test, an agent and the check each ask that system whether the change works. Its parts, each described in [[daworks/crate/README|the README]]:
- [[daworks/crate/README#A workspace|the workspace]]: the system of one change, made by `crate up`: its network, its services, the tools member and the app
- [[daworks/crate/README#crate.toml|crate.toml]]: a repo's declaration: its image, services, tools, app and dev image, and the merge queue's keys
- [[daworks/crate/README#The machine|the machine]]: the line, the order and the count crate keeps on its own work, so it is safe on a machine no one tuned
- [[daworks/crate/README#The dev image|the dev image]]: one image per hash of a repo's inputs, kept only when its smoke passes, and cached in the registry by that hash, which the merge queue alone writes
- [[daworks/crate/README#The merge queue|the merge queue]]: the only way main moves on the server: a change enters, is checked on the changes ahead of it, and lands when it passes; `crate land` is its client
- [[daworks/crate/README#The record|the record]]: one JSON line per step of each run, with its seconds, so a cost is read and not guessed
- [[daworks/crate/README#Changing crate|changing crate]]: crate's own check and release, outside the queue it ships

- [[daworks]]: the projects crate is one of
- [[ci]]: the stations a change passes, of which the merge queue is crate's
