package app_test

import (
	"regexp"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestCanvasRecoverRedisPasswords(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	otherEnv := k.project(canvasOther, "Theirs")
	m := canvasMember(canvasOrg)
	bare := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	seeded := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	valkey := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	svcRedis := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Image: canvasPtr("redis:7")})
	worker := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "worker"})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	lone := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "lone"})
	k.setVar(worker, "QUEUE_URL", "${{ redis.REDIS_URL }}")
	k.setVar(api, "Q", "${{ worker.QUEUE_URL }}")
	k.exec(`DELETE FROM variables WHERE node_id IN (?, ?)`, bare, valkey)
	k.exec(`UPDATE nodes SET desired_image = 'valkey/valkey:8' WHERE id = ?`, valkey)
	foreign, err := k.app.CreateNode(k.ctx, canvasMember(canvasOther), otherEnv, app.CreateNodeInput{Type: domain.NodeCache})
	if err != nil {
		t.Fatal(err)
	}
	k.exec(`DELETE FROM variables WHERE node_id = ?`, foreign.ID)
	k.exec(`UPDATE nodes SET dirty = 0`)
	seededPassword := k.vars(seeded)[0].Value
	k.pub.take(canvasOrg)
	k.pub.take(canvasOther)

	k.app.RecoverCanvas(k.ctx)

	for _, id := range []string{bare, foreign.ID} {
		vs := k.vars(id)
		if len(vs) != 1 || vs[0].Key != "REDIS_PASSWORD" || !vs[0].Secret || !regexp.MustCompile(`^[a-km-zA-HJ-NP-Z2-9]{20}$`).MatchString(vs[0].Value) {
			t.Fatalf("%s variables %+v", id, vs)
		}
		if !k.node(id).Dirty {
			t.Errorf("%s not staged", id)
		}
	}
	if vs := k.vars(seeded); len(vs) != 1 || vs[0].Value != seededPassword || k.node(seeded).Dirty {
		t.Errorf("a cache with a password was touched: %+v dirty %v", vs, k.node(seeded).Dirty)
	}
	for _, id := range []string{valkey, svcRedis, lone} {
		if len(k.vars(id)) != 0 || k.node(id).Dirty {
			t.Errorf("%s touched: %+v dirty %v", k.node(id).Name, k.vars(id), k.node(id).Dirty)
		}
	}
	if !k.node(worker).Dirty || !k.node(api).Dirty {
		t.Errorf("referrers: worker %v api %v", k.node(worker).Dirty, k.node(api).Dirty)
	}
	vars, _ := k.app.ListVariables(k.ctx, m, worker)
	if want := "redis://default:" + k.vars(bare)[0].Value + "@svc-" + bare + ":6379"; vars[0].Resolved != want || !vars[0].ResolvedSecret {
		t.Errorf("worker QUEUE_URL %q secret %v, want %q", vars[0].Resolved, vars[0].ResolvedSecret, want)
	}
	if topics := k.pub.take(canvasOrg); !canvasHas(topics, "/api/environments/"+env) || !canvasHas(topics, "/api/nodes/"+worker) {
		t.Errorf("topics %v", topics)
	}
	if topics := k.pub.take(canvasOther); !canvasHas(topics, "/api/environments/"+otherEnv) {
		t.Errorf("other organization's topics %v", topics)
	}

	password := k.vars(bare)[0].Value
	k.exec(`UPDATE nodes SET dirty = 0`)
	k.app.RecoverCanvas(k.ctx)
	if vs := k.vars(bare); len(vs) != 1 || vs[0].Value != password || k.node(bare).Dirty {
		t.Errorf("second run changed %+v", vs)
	}
	if topics := k.pub.take(canvasOrg); len(topics) != 0 {
		t.Errorf("second run published %v", topics)
	}
}
