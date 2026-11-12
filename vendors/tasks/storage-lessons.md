## 2026-11-12: Bounded work selection must prove progress under recurring work

Root-sorted PendingRoots with LIMIT repeatedly selects an early root that becomes
dirty between blocks, starving later roots despite correct snapshot frontiers.
Persist the last completed snapshot block and rotate serviced roots behind older
work. Test maxRoots1 with the first root always dirty, including previously
snapshotted waiting roots, not merely finite queues that eventually drain.
