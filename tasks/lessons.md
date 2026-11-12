# Retained engineering lessons

- Detach mutable event metadata before callbacks; preserve pre-hash normalized bytes.
- Read-stream callbacks run once; retry only side-effect-free known-abort writes.
- A durable exact receipt can reconcile uncertainty; absence is not proof of rollback.
- Bind derived requests to immutable intent, including historical source versions.
- Freeze source before recording full verification; source mappings are not behavioral proof.
- One network is one signing trust domain. Delivery identity is distinct from store-peer identity.

## 2026-11-12: Framework consumers own their applications

The user rejected keeping Dia domain/runtime code in Toolbox. Treat Dia as an
independent third-party consumer: Toolbox must build and test without importing
Dia or owning Dia policies, models, routes, or entrypoints. A thin application
wrapper does not establish that boundary. Preserve capability grouping in Go
folders; enforce dependency direction with a repository check and test the
application against the public framework module, not only a sibling workspace.
