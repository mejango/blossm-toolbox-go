# Retained engineering lessons

- Detach mutable event metadata before callbacks; preserve pre-hash normalized bytes.
- Read-stream callbacks run once; retry only side-effect-free known-abort writes.
- A durable exact receipt can reconcile uncertainty; absence is not proof of rollback.
- Bind derived requests to immutable intent, including historical source versions.
- Freeze source before recording full verification; source mappings are not behavioral proof.
- One network is one signing trust domain. Delivery identity is distinct from store-peer identity.
