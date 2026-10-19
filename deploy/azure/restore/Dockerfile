# One-off image that restores a pg_dump of the local database into the Azure PostgreSQL server. Built by
# deploy/azure/restore-db.ps1, which puts bookfinder.dump next to this file; run as a Container Apps job inside the
# private network, the only place the server is reachable from. Connection settings come from PG* variables.
FROM postgres:17-alpine
COPY bookfinder.dump /bookfinder.dump
# --clean --if-exists makes a re-run replace what a previous run restored. Ownership, privileges and comments are
# skipped: the Azure administrator is not a superuser and cannot set them on extensions.
CMD ["sh", "-c", "pg_restore --clean --if-exists --no-owner --no-privileges --no-comments --single-transaction --exit-on-error -d \"$PGDATABASE\" /bookfinder.dump && psql -c 'SELECT (SELECT count(*) FROM works) AS works, (SELECT count(*) FROM work_embeddings) AS vectors'"]
