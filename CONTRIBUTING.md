# Contributing to GPU Grid Worker

Thank you for auditing and contributing to `gpu-grid-worker`!

## Architectural & Code Standards

1. **`CGO_ENABLED=0` Portability**: All packages must compile cleanly for both `windows/amd64` and `linux/amd64` without C dependencies. Isolate OS-specific syscalls behind `//go:build` tags.
2. **Concentric Clean Architecture**:
   - `pkg/domain` and `pkg/protocol` depend strictly on pure Go standard library packages.
   - `pkg/application/worker` orchestrates `pkg/domain` and `pkg/protocol` without importing `pkg/infrastructure` or `pkg/presentation`.
   - `pkg/presentation` adapters never import `pkg/infrastructure`.
3. **Hard Complexity & Size Ceilings** (enforced by `tests/arch`):
   - Cyclomatic & Cognitive complexity `<= 15` per function.
   - `cmd/worker/*.go <= 200` LOC; `pkg/presentation/**/*.go <= 300` LOC.
   - Interfaces `<= 5` resolved methods.
   - Self-documenting Go code in English with zero inline prose comments (`//go:build` and `//go:embed` directives only) and zero silent blank error discards (`_ =`).
4. **Tests Under `tests/`**:
   - Place all `*_test.go` files under `tests/{arch,unit,integration}` using external black-box packages (`package foo_test`).

## Pre-Submission Checklist

```bash
gofmt -w cmd pkg tests
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -race ./...
```

By submitting a pull request, you agree that your contribution is licensed under the [Apache License, Version 2.0](./LICENSE).
