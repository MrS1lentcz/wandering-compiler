# UI Conventions (`ui/`)

The `ui/` application — React Native + Expo, TypeScript-only, web-first — with its own Docker/compose integration and an optional mobile distribution path.

## Stack

| Technology | Role |
|---|---|
| **React Native** | Component model — shared across web and mobile |
| **Expo** | Toolchain — build, dev server, web export, mobile distribution |
| **TypeScript** | Only language used in `ui/` — no plain JavaScript |
| **Expo Router** | File-based routing for both web and native |

## Web-First

The UI is designed and developed with **web as the primary target**. Mobile apps (iOS, Android) are an opt-in extension — each project decides independently whether and when to ship them.

This means:
- Web functionality is never blocked on mobile readiness
- Responsive layouts and native-specific adaptations are per-project decisions
- The `Dockerfile` and `compose.yaml` integration always targets the web build

## Structure

```
ui/
├── Dockerfile              # Locks Node/npm versions, handles compilation — dev by default
├── package.json
├── tsconfig.json
├── app/                    # Expo Router — screens and navigation (file-based routing)
│   ├── (tabs)/             # Tab navigator example
│   ├── _layout.tsx         # Root layout
│   └── …
├── components/             # Shared UI components
├── hooks/                  # Shared hooks
├── lib/                    # Utilities, API clients, helpers
└── assets/                 # Static assets (images, fonts, …)
```

## Docker and Compose

`ui/` always has a `Dockerfile` and runs as part of `compose.yaml` — zero dependencies on the host beyond Docker Desktop, consistent with all other project services.

The `Dockerfile` is primarily a **development and build environment** — it locks Node and npm versions, handles compilation, and ensures every developer runs the same setup regardless of their host machine. The default compose mode is **dev** (`expo start --web`).

Production deployments live in `infra/` with their own `compose.yaml` and optionally a separate `Dockerfile` when the production setup differs. The root `Dockerfile` is not assumed to be production-ready.

## Mobile Distribution

Mobile app distribution (App Store, Google Play) is **project-specific** and handled separately from the standard Docker/compose workflow. When a project decides to ship mobile apps, the recommended path is **EAS Build** (Expo Application Services) for cloud builds — no local Xcode or Android SDK required on developer machines.

Mobile distribution details must be documented in the project's `docs/` when applicable.

## Proto — `ui/` Types

`proto/domains/<domain>/ui/` contains REST API types specific to the UI layer. These types inherit from `services/` and `types/` and are generated into `ui/lib/pb/` (or equivalent) via `make schemagen`. Never edited manually.
