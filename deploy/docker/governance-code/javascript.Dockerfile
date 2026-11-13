ARG NODE_BASE_IMAGE=public.ecr.aws/docker/library/node@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205
FROM ${NODE_BASE_IMAGE}
WORKDIR /opt/datamesh
COPY deploy/docker/governance-code/package*.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY deploy/docker/governance-code/javascript.cjs ./javascript.cjs
USER 65532:65532
ENTRYPOINT ["node","--max-old-space-size=128","/opt/datamesh/javascript.cjs"]
