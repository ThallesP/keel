-- Canvas area. Node names are unique per environment: `${{ name.KEY }}` references resolve by
-- name (docs/go/spec/projects.md section 0). Convex enforced it with read-then-write only; the
-- use cases still check first and map a violation to the same message.
CREATE UNIQUE INDEX nodes_environment_name ON nodes(environment_id, name);
