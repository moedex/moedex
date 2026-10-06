# Captured container cleanup

`CapturedContainerCleanup` verifies and removes only the exact full Docker ID
written by a trusted host controller to `captures/<assignment>/container.id`.
The caller freezes that controller and owns the capture tree. Solver containers
must have no host capture mounts. This helper cannot authenticate a CID supplied
by an arbitrary untrusted writer.

Use `cleanup.close` as the bounded `OwnedWaves(terminal_check=...)` callback.
After the runner group stops and its actual exit is retained, the helper queries
only that full ID, removes it if present, and separately confirms absence. Each
Docker operation has a finite timeout. Daemon/context failure, ambiguous replies,
missing/malformed/symlink IDs and unknown closure never become success. Missing
CID alone does not prove no container was created. No global Docker cleanup is
performed and no unrelated container is selected.

A separate exclusive, fsynced mode-0600 receipt under `runtime/containers` records
assignment, CID, closure and fixed categories, without environment or stderr.
Physical cleanup precedes receipt writes; failed storage stays incomplete. The
original capture/inventory and actual runner exit are preserved.
The selected archive root, capture directories and receipt directories are
opened through no-follow directory descriptors; receipt-parent symlinks are
rejected without writing through them. The archive root must already exist.

Unit tests use neutral fake Docker replies. Set `MOEDEX_TEST_DOCKER_IMAGE` to an
already inspected immutable image to also test a network-disabled, read-only
neutral container that survives Docker CLI death and is closed by its exact CID.
No provider or native retrieval is used.
