# Snap release flow

_Last updated: 2026-06-16_

How the `wsstat` snap gets built, uploaded, and promoted.

## Summary

Snap revisions are produced **only by the `Manual Release` GitHub Actions workflow**
(`.github/workflows/release.yml`), never automatically on `main` pushes. Each release
uploads one revision to the store and releases it to `latest/edge`. Promotion to
`beta`, `candidate`, and `stable` is done by hand from the Snapcraft web UI.

## Why it works this way

The Snapcraft build service's linked-repo feature auto-builds on **every push to the
tracked branch** and serializes builds, so a `main` merge would cancel an in-flight
release build. There is no "tags only" option in that dashboard, we therefore
**disconnected the linked repo** and now drive builds from CI instead, gated on an
intentional release.

If you ever see stray edge revisions labelled `vX.Y.Z-N-g<sha>` appearing outside a
release run, the linked repo has been reconnected and you might consider disconnecting
it again (snap dashboard → Builds → "Disconnect repo").

## Prerequisite (one-time setup)

- [ ] **Add the `SNAPCRAFT_STORE_CREDENTIALS` GitHub Actions secret**
      (repo → Settings → Secrets and variables → Actions). Generate the value with:

      snapcraft export-login --snaps wsstat --channels edge --acls package_upload -

      Scoped to upload + edge only (cannot promote to stable). Rotate periodically, or
      add `--expires YYYY-MM-DD`. If missing or expired, the `snap` job fails loudly
      while the GitHub release itself still succeeds.

## Pipeline

1. Bump `VERSION` and merge to `main`.
2. Trigger the `Manual Release` workflow (`workflow_dispatch`), optionally ticking
   _prerelease_ to cut an `-rc.N` tag.
3. The workflow tests, lints, creates and pushes the git tag, and publishes the
   GitHub Release.
4. The `snap` job checks out the freshly pushed tag, runs `snapcore/action-build`,
   then `snapcore/action-publish` with `release: edge`.
5. The new revision lands on `latest/edge` and appears under "Revisions available to
   release" in the web UI.

## Promotion (manual)

Snap dashboard → **Releases**. The revisions list is shared store state, independent of
how a revision was built. Drag (or use the Release button on) the new revision up to
`candidate`/`stable` when you're satisfied with it.

## Configuration

- **Store credentials:** repo secret `SNAPCRAFT_STORE_CREDENTIALS` — see
  [Prerequisite](#prerequisite-one-time-setup). Promotion uses your own dashboard
  session, so CI never needs rights beyond upload + edge.
- **Version label:** `snap/snapcraft.yaml`'s `override-pull` runs
  `git describe --tags --abbrev=7`. The `snap` job checks out the release tag first so
  the revision reads as the clean tag (e.g. `v2.2.1`) rather than a `-N-g<sha>` describe
  from a later commit. This is why the job needs `fetch-depth: 0`.
