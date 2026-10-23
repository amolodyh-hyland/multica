# Local fork server images

This path uses the existing backend and web Dockerfiles and the self-host recipe. It creates no migrations. A source manifest records the full commit, tree, archive checksum, file checksums, build arguments and rendered recipe checksum. The image manifest records matching revision labels, local image IDs and the selected engine. Tags identify the source; image IDs identify the actual build. Rebuilding the same source can produce different image IDs because base image tags and package repositories can change.

## Prepare without Docker

From this fork checkout, after committing the intended source:

```sh
REV=$(git rev-parse HEAD)
INPUTS="$HOME/multica-fork-$REV"
node scripts/fork-images.mjs prepare --source "$REV" --output "$INPUTS"
```

The output must be a new absolute directory outside this checkout. Only the named commit enters `source/`; local changes and untracked files do not. `stack/docker-compose.selfhost.yml` is the image-only deployment recipe. Its defaults select `local/multica-backend:fork-<full SHA>` and `local/multica-web:fork-<full SHA>`. It keeps the fixed `multica` project, bare named volumes and loopback-only ports. The existing development build override is unsuitable for the local server controls and remains rejected.

## Build and inspect — human approval required

Obtain approval for the two builds, their downloads, disk usage and the PostgreSQL pull before running this block. No image is published. On the supported Mac, choose the same local socket that the local server controls select (first existing socket: `~/.docker/run/docker.sock`, `/var/run/docker.sock`, `~/.orbstack/run/docker.sock`, `~/.colima/default/docker.sock`, `~/.rd/docker.sock`). Do not substitute a remote context.

```sh
ENGINE="unix://$HOME/.docker/run/docker.sock" # replace only if the selected socket differs
. "$INPUTS/build.env"
docker --host "$ENGINE" build --file "$INPUTS/source/Dockerfile" \
  --build-arg VERSION="$MULTICA_IMAGE_TAG" --build-arg COMMIT="$MULTICA_SOURCE" \
  --build-arg DATE=1970-01-01T00:00:00Z \
  --label "org.opencontainers.image.revision=$MULTICA_SOURCE" \
  --tag "$MULTICA_BACKEND_IMAGE:$MULTICA_IMAGE_TAG" "$INPUTS/source"
docker --host "$ENGINE" build --file "$INPUTS/source/Dockerfile.web" \
  --build-arg NEXT_PUBLIC_APP_VERSION="$MULTICA_IMAGE_TAG" \
  --label "org.opencontainers.image.revision=$MULTICA_SOURCE" \
  --tag "$MULTICA_WEB_IMAGE:$MULTICA_IMAGE_TAG" "$INPUTS/source"
docker --host "$ENGINE" pull pgvector/pgvector:pg17
node scripts/fork-images.mjs manifest --inputs "$INPUTS" --engine "$ENGINE"
```

Keep both manifests. Compare the image IDs in the manifest with `docker --host "$ENGINE" image inspect` immediately before activation. A retag, rebuild, changed engine, source or recipe requires renewed inspection. The image variables contain repository names, without tags or digests: the recipe appends `:${MULTICA_IMAGE_TAG}`. A digest pasted into an image-name variable is invalid. Overrides must still name the inspected fork images; do not select `latest` or upstream images.

## Activate — separate human approval required

Approve the exact stack directory, ports, data location, supported email sign-in configuration and server activation. Before activation, verify migration/startup, health and sign-in against disposable data under a separately approved test procedure. A fixed development sign-in code belongs only in disposable fixtures. Use an existing approved SMTP/email service for usable data; do not weaken authentication.

In the desktop Settings, select the local server at `http://localhost:3000` with explicit API origin `http://localhost:8080`, choose `$INPUTS/stack`, and use **Set up** to create a private `.env` with fresh secrets. Edit that file for the approved ports/origins and SMTP settings. Leave `FF_PUBLIC_DELEGATIONS_V1=false`; setting it true forwards it to the backend, but enabling admission needs separate approval. Server activation does not enable the desktop delegation setting, install a runtime or authorize a task.

Click **Start server**. The local controls run:

```sh
docker compose -f "$INPUTS/stack/docker-compose.selfhost.yml" \
  --project-directory "$INPUTS/stack" -p multica up -d --no-build --pull never
```

This is the command the controls execute **after** their preflight; do not run it manually to bypass that gate. Builds and pulls cannot happen implicitly. A project in another folder, with other ports or fresh volumes, is still the same `-p multica` project. Any existing containers (including stopped ones), retained volumes or networks require the separate takeover/recovery flow described in the matching desktop fork's `docs/multica-local-server-takeover.md`. Missing ownership or recovery evidence blocks start, stop and restart. Do not run `compose down` or remove volumes as a workaround.

No real stack, database, sign-in or image build was exercised by the mocked checks for this change. Those remain operator verification and approval steps.
