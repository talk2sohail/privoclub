# syntax=docker/dockerfile:1
# The documentation site, docs.privo.club (docs/documentation-site.md, 2.6 B).
# Build from the repository root: docker build -f docs/Dockerfile .
# The root .dockerignore lets docs/site and these files through.

# python:3.12-slim on 2026-11-22
FROM python:3.12-slim@sha256:5e2bdb1b0d542d5039cffb8181c65930093949194a65c365dfb1b0fe13a4fa5b AS build
WORKDIR /src
COPY docs/requirements.txt docs/requirements.txt
# Pinned and hashed, wheels only: no package runs code at install time.
RUN pip install --no-cache-dir --root-user-action=ignore --disable-pip-version-check \
      --require-hashes --only-binary=:all: -r docs/requirements.txt
COPY mkdocs.yml ./
COPY docs/site docs/site
COPY docs/externalize_inline_scripts.py docs/
RUN mkdocs build --strict && python docs/externalize_inline_scripts.py build/site

# caddy:2-alpine (v2.11.9) on 2026-11-24
FROM caddy@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0
COPY docs/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/build/site /srv
ENV XDG_CONFIG_HOME=/tmp XDG_DATA_HOME=/tmp
USER nobody
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1
