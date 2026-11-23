# Not built. Dependabot's docker updater reads it, so it bumps PostgresImage,
# which no Dockerfile here pulls: new apps' compose.yml and the workflows'
# Postgres service have it. internal/pins's test holds pins.go and those
# copies to this line; a bump here fails it until they match.
FROM postgres:17@sha256:223efeb196329b55b3b9a13db144956411dbc18e727e59f681c3e0aede6475cf
