package app_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestCanvasCreateNodeDefaults(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	cases := []struct {
		in       app.CreateNodeInput
		name     string
		desired  *domain.Desired
		dirty    bool
		vars     string
		config   domain.NodeConfig
		position domain.Position
	}{
		{app.CreateNodeInput{Type: domain.NodeService}, "nginx", &domain.Desired{Image: "nginx:alpine", Replicas: 1, Port: 80}, true, "", domain.NodeConfig{}, domain.Position{X: 0, Y: 0}},
		{app.CreateNodeInput{Type: domain.NodeService, Image: new("ghcr.io/acme/api-server:1.2")}, "api-server", &domain.Desired{Image: "ghcr.io/acme/api-server:1.2", Replicas: 1, Port: 80}, true, "", domain.NodeConfig{}, domain.Position{X: 280}},
		{app.CreateNodeInput{Type: domain.NodeDatabase}, "postgres", &domain.Desired{Image: "postgres:16", Replicas: 1, Port: 5432}, true, "POSTGRES_USER,POSTGRES_PASSWORD,POSTGRES_DB", domain.NodeConfig{}, domain.Position{X: 560}},
		{app.CreateNodeInput{Type: domain.NodeDatabase, Engine: domain.EngineMySQL}, "mysql", &domain.Desired{Image: "mysql:8", Replicas: 1, Port: 3306}, true, "MYSQL_ROOT_PASSWORD,MYSQL_USER,MYSQL_PASSWORD,MYSQL_DATABASE", domain.NodeConfig{}, domain.Position{X: 840}},
		{app.CreateNodeInput{Type: domain.NodeDatabase, Engine: domain.EngineMongo, Port: new(27018.0)}, "mongo", &domain.Desired{Image: "mongo:7", Replicas: 1, Port: 27018}, true, "MONGO_INITDB_ROOT_USERNAME,MONGO_INITDB_ROOT_PASSWORD", domain.NodeConfig{}, domain.Position{X: 1120}},
		{app.CreateNodeInput{Type: domain.NodeCache}, "redis", &domain.Desired{Image: "redis:7", Replicas: 1, Port: 6379}, true, "REDIS_PASSWORD", domain.NodeConfig{}, domain.Position{X: 1400}},
		{app.CreateNodeInput{Type: domain.NodeCache, Engine: domain.EngineRedis}, "redis-2", &domain.Desired{Image: "redis:7", Replicas: 1, Port: 6379}, true, "REDIS_PASSWORD", domain.NodeConfig{}, domain.Position{X: 1680}},
		{app.CreateNodeInput{Type: domain.NodeVolume}, "data", nil, false, "", domain.NodeConfig{SizeGb: new(10.0)}, domain.Position{X: 1960}},
		{app.CreateNodeInput{Type: domain.NodeGroup, Position: &domain.Position{X: 5.5, Y: -3}}, "group", nil, false, "", domain.NodeConfig{Width: new(300.0), Height: new(180.0)}, domain.Position{X: 5.5, Y: -3}},
		{app.CreateNodeInput{Type: domain.NodeService, Name: "api", Replicas: new(0.0)}, "api", &domain.Desired{Image: "nginx:alpine", Port: 80}, true, "", domain.NodeConfig{}, domain.Position{X: 2240}},
	}
	var ids []string
	for _, c := range cases {
		id := k.create(env, c.in)
		ids = append(ids, id)
		n := k.node(id)
		if n.Name != c.name || n.Dirty != c.dirty || !reflect.DeepEqual(n.Config, c.config) || n.Position != c.position {
			t.Errorf("%s: got name %q dirty %v config %+v position %+v", c.name, n.Name, n.Dirty, n.Config, n.Position)
		}
		if !reflect.DeepEqual(n.Desired, c.desired) {
			t.Errorf("%s: desired %+v", c.name, n.Desired)
		}
		if got := canvasKeys(k.vars(id)); got != c.vars {
			t.Errorf("%s: variables %s, want %s", c.name, got, c.vars)
		}
	}
	if k.vars(ids[5])[0].Value == k.vars(ids[6])[0].Value {
		t.Error("two redis nodes share a password")
	}
	nodes, err := k.app.ListNodes(k.ctx, canvasMember(canvasOrg), env)
	if err != nil || len(nodes) != len(ids) {
		t.Fatalf("list: %d %v", len(nodes), err)
	}
	for i, n := range nodes {
		if n.ID != ids[i] {
			t.Fatalf("order: %d is %s, want %s", i, n.Name, cases[i].name)
		}
	}
	if len(k.ships) != 0 {
		t.Errorf("ships without deploy: %+v", k.ships)
	}
}

func TestCanvasCreateNodeErrors(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	k.create(env, app.CreateNodeInput{Type: domain.NodeService})
	cases := []struct {
		name string
		in   app.CreateNodeInput
		code string
		msg  string
	}{
		{"image on a database", app.CreateNodeInput{Type: domain.NodeDatabase, Image: new("postgres:15"), Port: new(1.5)}, domain.CodeInvalidInput, "Only services take a custom image"},
		{"empty image is an image", app.CreateNodeInput{Type: domain.NodeService, Image: new("")}, domain.CodeInvalidInput, "Image must look like repo/name:tag"},
		{"port on a volume", app.CreateNodeInput{Type: domain.NodeVolume, Port: new(80.0)}, domain.CodeInvalidInput, "This node type has no runtime settings"},
		{"replicas on a group", app.CreateNodeInput{Type: domain.NodeGroup, Replicas: new(1.0)}, domain.CodeInvalidInput, "This node type has no runtime settings"},
		{"engine of another type", app.CreateNodeInput{Type: domain.NodeDatabase, Engine: domain.EngineRedis}, domain.CodeInvalidInput, "redis is not a database"},
		{"engine on a service", app.CreateNodeInput{Type: domain.NodeService, Engine: domain.EnginePostgres}, domain.CodeInvalidInput, "postgres is not a service"},
		{"bad image", app.CreateNodeInput{Type: domain.NodeService, Image: new("Nginx:Latest")}, domain.CodeInvalidInput, "Image must look like repo/name:tag"},
		{"taken before invalid", app.CreateNodeInput{Type: domain.NodeService, Name: "nginx", Port: new(0.0)}, domain.CodeNameTaken, `"nginx" is already taken`},
		{"bad name", app.CreateNodeInput{Type: domain.NodeService, Name: "Api"}, domain.CodeInvalidInput, "Name: 1–40 chars, a-z 0-9 and - only"},
		{"long name", app.CreateNodeInput{Type: domain.NodeService, Name: strings.Repeat("a", 41)}, domain.CodeInvalidInput, "Name: 1–40 chars, a-z 0-9 and - only"},
		{"replicas before port", app.CreateNodeInput{Type: domain.NodeService, Replicas: new(21.0), Port: new(0.0)}, domain.CodeInvalidInput, "Replicas must be 0–20"},
		{"fractional port", app.CreateNodeInput{Type: domain.NodeService, Port: new(80.5)}, domain.CodeInvalidInput, "Port must be 1–65535"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, c.in)
			canvasWantErr(t, err, c.code, c.msg)
		})
	}
	if nodes, _ := k.app.ListNodes(k.ctx, canvasMember(canvasOrg), env); len(nodes) != 1 {
		t.Errorf("failed creates left %d nodes", len(nodes))
	}
	_, err := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), "nope", app.CreateNodeInput{Type: domain.NodeService})
	canvasWantErr(t, err, domain.CodeProjectNotFound, "Environment not found")
}

func TestCanvasCreateNodeDeploy(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	out, err := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, app.CreateNodeInput{Type: domain.NodeService, Deploy: true})
	if err != nil || out.DeploymentID != "dep-"+env {
		t.Fatalf("deploy: %+v %v", out, err)
	}
	if len(k.ships) != 1 || !reflect.DeepEqual(k.ships[0], app.ShipOptions{Only: []string{out.ID}}) {
		t.Fatalf("ship call %+v", k.ships)
	}
	if out, _ := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, app.CreateNodeInput{Type: domain.NodeVolume, Deploy: true}); out.DeploymentID != "" || len(k.ships) != 1 {
		t.Fatalf("volume shipped: %+v", out)
	}
	k.shipErr = domain.E(domain.CodeDeploymentRunning, "A deployment is already running")
	out, err = k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, app.CreateNodeInput{Type: domain.NodeService, Deploy: true})
	if err != nil || out.DeploymentID != "" || !k.node(out.ID).Dirty {
		t.Fatalf("refused ship: %+v %v", out, err)
	}
	k.shipErr = errors.New("disk on fire")
	if _, err := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, app.CreateNodeInput{Type: domain.NodeService, Name: "boom", Deploy: true}); err == nil {
		t.Fatal("no error")
	}
	nodes, _ := k.app.ListNodes(k.ctx, canvasMember(canvasOrg), env)
	if slices.ContainsFunc(nodes, func(n domain.Node) bool { return n.Name == "boom" }) {
		t.Fatal("create not rolled back")
	}
}

func TestCanvasRenameNode(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	k.setVar(api, "DB", "${{postgres.DATABASE_URL}} ${{ other.X }}")
	k.setVar(pg, "SELF", "${{ POSTGRES_USER }}/${{ postgres.POSTGRES_DB }}")
	k.clean(env)
	k.pub.take(canvasOrg)

	if err := k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), pg, app.NodeUpdate{Name: new("db")}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(pg); n.Name != "db" || n.Dirty {
		t.Fatalf("renamed node %q dirty=%v (a rename is not a staged change)", n.Name, n.Dirty)
	}
	if v := k.vars(api)[0].Value; v != "${{ db.DATABASE_URL }} ${{ other.X }}" {
		t.Errorf("referrer = %q", v)
	}
	if v := k.vars(pg)[3].Value; v != "${{ POSTGRES_USER }}/${{ db.POSTGRES_DB }}" {
		t.Errorf("self = %q", v)
	}
	if k.node(api).Dirty {
		t.Error("referrer dirty after a rename")
	}
	topics := k.pub.take(canvasOrg)
	for _, want := range []string{"/api/environments/" + env, "/api/nodes/" + api, "/api/nodes/" + pg} {
		if !slices.Contains(topics, want) {
			t.Errorf("topics %v lack %s", topics, want)
		}
	}

	if err := k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Name: new("api")}); err != nil {
		t.Fatal(err)
	}
	if got := k.pub.take(canvasOrg); len(got) != 0 {
		t.Errorf("no-op published %v", got)
	}
	err := k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Name: new("db")})
	canvasWantErr(t, err, domain.CodeNameTaken, `"db" is already taken`)
	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Name: new("no spaces")})
	canvasWantErr(t, err, domain.CodeInvalidInput, "Name: 1–40 chars, a-z 0-9 and - only")
	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), "nope", app.NodeUpdate{Name: new("x")})
	canvasWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
}

func TestCanvasSetDesired(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	redis := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	worker := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "worker"})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	other := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "other"})
	vol := k.create(env, app.CreateNodeInput{Type: domain.NodeVolume})
	k.setVar(worker, "QUEUE_URL", "${{ redis.REDIS_URL }}")
	k.setVar(api, "Q", "${{ worker.QUEUE_URL }}")
	k.exec(`UPDATE nodes SET desired_revision = 3, desired_tracing = 1 WHERE id = ?`, redis)
	k.clean(env)

	err := k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), redis, app.NodeUpdate{Port: new(6380.0), Replicas: new(2.0)})
	if err != nil {
		t.Fatal(err)
	}
	n := k.node(redis)
	if !n.Dirty || n.Desired.Port != 6380 || n.Desired.Replicas != 2 || n.Desired.Image != "redis:7" || n.Desired.Revision != 3 || !n.Desired.Tracing {
		t.Fatalf("desired %+v dirty %v", n.Desired, n.Dirty)
	}
	if !k.node(worker).Dirty || !k.node(api).Dirty || k.node(other).Dirty {
		t.Errorf("dirty: worker %v api %v other %v", k.node(worker).Dirty, k.node(api).Dirty, k.node(other).Dirty)
	}
	k.clean(env)
	if err := k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), other, app.NodeUpdate{Image: new("nginx:alpine")}); err != nil || !k.node(other).Dirty {
		t.Fatalf("same image: dirty %v %v", k.node(other).Dirty, err)
	}

	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), vol, app.NodeUpdate{Image: new("x")})
	canvasWantErr(t, err, domain.CodeInvalidInput, "This node type has no runtime settings")
	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Image: new("UPPER"), Port: new(0.0)})
	canvasWantErr(t, err, domain.CodeInvalidInput, "Image must look like repo/name:tag")
	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Port: new(70000.0), Replicas: new(99.0)})
	canvasWantErr(t, err, domain.CodeInvalidInput, "Port must be 1–65535")
	err = k.app.UpdateNode(k.ctx, canvasMember(canvasOrg), api, app.NodeUpdate{Name: new("api-2"), Replicas: new(99.0)})
	canvasWantErr(t, err, domain.CodeInvalidInput, "Replicas must be 0–20")
	if k.node(api).Name != "api" {
		t.Error("rename kept after a failed update")
	}
}

func TestCanvasConfigAndParent(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	group := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup, Position: &domain.Position{X: 100, Y: 50}})
	group2 := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup, Position: &domain.Position{X: 1000, Y: 0}})
	vol := k.create(env, app.CreateNodeInput{Type: domain.NodeVolume, Position: &domain.Position{X: 130, Y: 90}})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Position: &domain.Position{X: 0, Y: 0}})
	k.clean(env)
	m := canvasMember(canvasOrg)

	if err := k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{Config: &domain.NodeConfig{SizeGb: new(25.0)}}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); *n.Config.SizeGb != 25 || n.Dirty {
		t.Fatalf("volume config %+v dirty %v", n.Config, n.Dirty)
	}
	if err := k.app.UpdateNode(k.ctx, m, group, app.NodeUpdate{Config: &domain.NodeConfig{Width: new(640.0)}}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(group); *n.Config.Width != 640 || *n.Config.Height != 180 {
		t.Fatalf("group config %+v", n.Config)
	}
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, api, app.NodeUpdate{Config: &domain.NodeConfig{SizeGb: new(1.0)}}), domain.CodeInvalidInput, "Only volumes have a size")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{Config: &domain.NodeConfig{Height: new(1.0)}}), domain.CodeInvalidInput, "Only groups have a width and height")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{Config: &domain.NodeConfig{SizeGb: new(0.0)}}), domain.CodeInvalidInput, "Size must be more than 0 GB")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, group, app.NodeUpdate{Config: &domain.NodeConfig{Height: new(-1.0)}}), domain.CodeInvalidInput, "Width and height must be more than 0")

	if err := k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: &group}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); n.ParentID != group || n.Position != (domain.Position{X: 30, Y: 40}) {
		t.Fatalf("in group: parent %q at %+v", n.ParentID, n.Position)
	}
	if err := k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: &group2}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); n.ParentID != group2 || n.Position != (domain.Position{X: -870, Y: 90}) {
		t.Fatalf("in group 2: parent %q at %+v", n.ParentID, n.Position)
	}
	if err := k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: &group, Position: &domain.Position{X: 1, Y: 2}}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); n.ParentID != group || n.Position != (domain.Position{X: 1, Y: 2}) {
		t.Fatalf("explicit: parent %q at %+v", n.ParentID, n.Position)
	}
	if err := k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: new("")}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); n.ParentID != "" || n.Position != (domain.Position{X: 101, Y: 52}) {
		t.Fatalf("top level: parent %q at %+v", n.ParentID, n.Position)
	}
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, group2, app.NodeUpdate{ParentID: &group}), domain.CodeInvalidInput, "Groups cannot be nested")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: &api}), domain.CodeInvalidInput, "Parent must be a group in the same environment")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: new("nope")}), domain.CodeInvalidInput, "Parent must be a group in the same environment")
	otherEnv := k.project(canvasOrg, "Other")
	foreignGroup := k.create(otherEnv, app.CreateNodeInput{Type: domain.NodeGroup})
	canvasWantErr(t, k.app.UpdateNode(k.ctx, m, vol, app.NodeUpdate{ParentID: &foreignGroup}), domain.CodeInvalidInput, "Parent must be a group in the same environment")
	for _, id := range []string{group, group2, vol, api} {
		if k.node(id).Dirty {
			t.Errorf("%s dirty after config/parent changes", k.node(id).Name)
		}
	}
}

func TestCanvasMoveNode(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService})
	k.clean(env)
	k.pub.take(canvasOrg)
	if err := k.app.MoveNode(k.ctx, canvasMember(canvasOrg), api, domain.Position{X: 12.75, Y: -4.5}); err != nil {
		t.Fatal(err)
	}
	if n := k.node(api); n.Position != (domain.Position{X: 12.75, Y: -4.5}) || n.Dirty {
		t.Fatalf("moved to %+v dirty %v", n.Position, n.Dirty)
	}
	if got := k.pub.take(canvasOrg); !reflect.DeepEqual(got, []string{"/api/environments/" + env + "/nodes"}) {
		t.Errorf("move topics %v (only the canvas list)", got)
	}
	canvasWantErr(t, k.app.MoveNode(k.ctx, canvasMember(canvasOrg), "nope", domain.Position{}), domain.CodeServiceNotFound, "Node not found")
}

func TestCanvasDuplicateNode(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	group := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup})
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase, Position: &domain.Position{X: 10, Y: 20}})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	vol := k.create(env, app.CreateNodeInput{Type: domain.NodeVolume})
	k.setVar(pg, "EXTRA", "${{ api.URL }}")
	k.exec(`UPDATE nodes SET parent_id = ?, desired_revision = 4, desired_tracing = 1, one_shot = 1, observed_at = 5, observed_revision = 4,
		observed_running = 1, observed_state = 'ok', deployed_revision = 4, apply_error = 'x', shipped_at = 9, dirty = 0 WHERE id = ?`, group, pg)
	k.exec(`INSERT INTO endpoints (id, node_id, ord, protocol, port, public_port, status_state, status_at) VALUES ('ep', ?, 0, 'tcp', 5432, 5432, 'live', 1)`, pg)

	copyID, err := k.app.DuplicateNode(k.ctx, canvasMember(canvasOrg), pg)
	if err != nil {
		t.Fatal(err)
	}
	c, orig := k.node(copyID), k.node(pg)
	if c.Name != "postgres-copy" || c.Type != domain.NodeDatabase || c.ParentID != group || c.Position != (domain.Position{X: 50, Y: 60}) {
		t.Fatalf("copy %+v", c)
	}
	if c.Desired == nil || c.Desired.Revision != 0 || c.Desired.Image != "postgres:16" || c.Desired.Port != 5432 || !c.Desired.Tracing {
		t.Fatalf("copy desired %+v", c.Desired)
	}
	if !c.Dirty || !c.OneShot || c.Observed != nil || c.DeployedRevision != 0 || c.ApplyError != "" || c.ShippedAt != nil || len(c.Endpoints) != 0 {
		t.Fatalf("copy state %+v", c)
	}
	cv, ov := k.vars(copyID), k.vars(pg)
	if len(cv) != len(ov) {
		t.Fatalf("variables %d vs %d", len(cv), len(ov))
	}
	for i := range ov {
		if cv[i].Key != ov[i].Key || cv[i].Value != ov[i].Value || cv[i].Secret != ov[i].Secret || cv[i].ID == ov[i].ID {
			t.Errorf("variable %d: %+v vs %+v", i, cv[i], ov[i])
		}
	}
	if orig.Name != "postgres" || len(orig.Endpoints) != 1 {
		t.Errorf("original changed: %+v", orig)
	}
	if id, _ := k.app.DuplicateNode(k.ctx, canvasMember(canvasOrg), pg); k.node(id).Name != "postgres-copy-2" {
		t.Errorf("second copy %q", k.node(id).Name)
	}
	if id, _ := k.app.DuplicateNode(k.ctx, canvasMember(canvasOrg), vol); k.node(id).Dirty || k.node(id).Name != "data-copy" {
		t.Errorf("volume copy %+v", k.node(id))
	}
	_, err = k.app.DuplicateNode(k.ctx, canvasMember(canvasOrg), group)
	canvasWantErr(t, err, domain.CodeInvalidInput, "Groups cannot be duplicated")
	k.exec(`UPDATE nodes SET name = ? WHERE id = ?`, strings.Repeat("n", 40), api)
	if id, _ := k.app.DuplicateNode(k.ctx, canvasMember(canvasOrg), api); k.node(id).Name != strings.Repeat("n", 32)+"-copy" {
		t.Errorf("long copy %q", k.node(id).Name)
	}
}

func TestCanvasStartStop(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Replicas: new(3.0)})
	vol := k.create(env, app.CreateNodeInput{Type: domain.NodeVolume})
	m := canvasMember(canvasOrg)

	id, err := k.app.StartNode(k.ctx, m, api)
	if err != nil || id != "dep-"+env {
		t.Fatalf("start: %q %v", id, err)
	}
	if got := k.ships[0]; !reflect.DeepEqual(got, app.ShipOptions{Only: []string{api}, Verb: "deploy"}) {
		t.Errorf("start ship %+v", got)
	}
	if n := k.node(api); n.Desired.Replicas != 3 || !n.Dirty {
		t.Errorf("after start %+v", n.Desired)
	}
	k.exec(`UPDATE nodes SET desired_revision = 1, dirty = 0 WHERE id = ?`, api)

	did, ok, err := k.app.StopNode(k.ctx, m, api)
	if err != nil || !ok || did != "dep-"+env {
		t.Fatalf("stop: %q %v %v", did, ok, err)
	}
	if got := k.ships[1]; !reflect.DeepEqual(got, app.ShipOptions{Only: []string{api}, Verb: "stop"}) {
		t.Errorf("stop ship %+v", got)
	}
	if n := k.node(api); n.Desired.Replicas != 0 || !n.Dirty {
		t.Errorf("after stop %+v", n.Desired)
	}
	if did, ok, err := k.app.StopNode(k.ctx, m, api); err != nil || ok || did != "" || len(k.ships) != 2 {
		t.Fatalf("stop again: %q %v %v", did, ok, err)
	}
	if _, err := k.app.StartNode(k.ctx, m, api); err != nil {
		t.Fatal(err)
	}
	if n := k.node(api); n.Desired.Replicas != 1 || k.ships[2].Verb != "start" {
		t.Errorf("restart: replicas %d verb %q", n.Desired.Replicas, k.ships[2].Verb)
	}
	k.exec(`UPDATE nodes SET dirty = 0 WHERE id = ?`, api)
	k.shipErr = domain.E(domain.CodeDeploymentRunning, "A deployment is already running")
	_, _, err = k.app.StopNode(k.ctx, m, api)
	canvasWantErr(t, err, domain.CodeDeploymentRunning, "A deployment is already running")
	if n := k.node(api); n.Desired.Replicas != 1 || n.Dirty {
		t.Errorf("stop not rolled back: %+v dirty %v", n.Desired, n.Dirty)
	}
	k.shipErr = nil

	_, err = k.app.StartNode(k.ctx, m, vol)
	canvasWantErr(t, err, domain.CodeInvalidInput, "This node type cannot be started")
	_, _, err = k.app.StopNode(k.ctx, m, vol)
	canvasWantErr(t, err, domain.CodeInvalidInput, "This node type cannot be stopped")
	_, err = k.app.StartNode(k.ctx, m, "nope")
	canvasWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
}

func TestCanvasRemoveNode(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	group := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup, Position: &domain.Position{X: 100, Y: 200}})
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	web := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "web"})
	vol := k.create(env, app.CreateNodeInput{Type: domain.NodeVolume, Position: &domain.Position{X: 5, Y: 6}})
	k.exec(`UPDATE nodes SET parent_id = ? WHERE id = ?`, group, vol)
	k.setVar(api, "DB", "${{ postgres.DATABASE_URL }}")
	k.setVar(web, "API", "${{ api.URL }}")
	k.exec(`INSERT INTO endpoints (id, node_id, ord, protocol, port, public_port, status_state, status_at) VALUES ('ep', ?, 0, 'tcp', 5432, 5432, 'live', 1)`, pg)
	k.clean(env)
	k.pub.take(canvasOrg)

	if err := k.app.RemoveNode(k.ctx, m, pg); err != nil {
		t.Fatal(err)
	}
	if k.exists(pg) || len(k.vars(pg)) != 0 {
		t.Fatal("node or its variables left")
	}
	if !k.node(api).Dirty || !k.node(web).Dirty {
		t.Error("referrers not staged")
	}
	if k.proxy != 1 || !reflect.DeepEqual(k.removed, []string{pg}) || !reflect.DeepEqual(k.observed, []string{pg}) {
		t.Errorf("after commit: proxy %d removed %v observed %v", k.proxy, k.removed, k.observed)
	}
	if topics := k.pub.take(canvasOrg); !slices.Contains(topics, "/api/nodes/"+pg) || !slices.Contains(topics, "/api/environments/"+env) || !slices.Contains(topics, "/api/nodes/"+web) {
		t.Errorf("topics %v", topics)
	}
	vars, _ := k.app.ListVariables(k.ctx, m, api)
	if vars[0].Resolved != "" || !vars[0].Parts[0].Ref.Missing {
		t.Errorf("dangling reference %+v", vars[0])
	}

	if err := k.app.RemoveNode(k.ctx, m, group); err != nil {
		t.Fatal(err)
	}
	if n := k.node(vol); n.ParentID != "" || n.Position != (domain.Position{X: 105, Y: 206}) {
		t.Errorf("child %q at %+v", n.ParentID, n.Position)
	}
	if k.proxy != 1 || len(k.removed) != 1 {
		t.Errorf("group delete scheduled work: %d %v", k.proxy, k.removed)
	}
	for _, id := range []string{"nope", pg} {
		if err := k.app.RemoveNode(k.ctx, m, id); err != nil {
			t.Errorf("remove %s: %v", id, err)
		}
	}
	canvasWantErr(t, k.app.RemoveNode(k.ctx, domain.Actor{}, api), domain.CodeNotAuthenticated, "Not authenticated")
}

func TestCanvasSummary(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService})
	k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	k.create(env, app.CreateNodeInput{Type: domain.NodeVolume})
	group := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup})
	k.exec(`UPDATE nodes SET dirty = 1 WHERE id = ?`, group)
	k.exec(`UPDATE nodes SET desired_revision = 1, dirty = 0, observed_at = 1, observed_revision = 1, observed_running = 1,
		observed_state = 'ok' WHERE id = ?`, api)

	s, err := k.app.EnvironmentSummaryOf(k.ctx, m, env)
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	want := app.EnvironmentSummary{PendingChanges: 1, Counts: map[domain.NodeStatus]int{domain.StatusHealthy: 1, domain.StatusPending: 2}, Servers: 0}
	if !reflect.DeepEqual(*s, want) {
		t.Errorf("summary %+v, want %+v", *s, want)
	}
	k.exec(`INSERT INTO cluster (id, servers, at) VALUES (1, 3, 1)`)
	if s, _ := k.app.EnvironmentSummaryOf(k.ctx, m, env); s.Servers != 3 {
		t.Errorf("servers %d", s.Servers)
	}
	for _, a := range []domain.Actor{{}, canvasMember(canvasOther)} {
		if s, err := k.app.EnvironmentSummaryOf(k.ctx, a, env); s != nil || err != nil {
			t.Errorf("%+v sees %+v %v", a, s, err)
		}
	}
	if s, err := k.app.EnvironmentSummaryOf(k.ctx, m, "nope"); s != nil || err != nil {
		t.Errorf("missing env %+v %v", s, err)
	}
}

func TestCanvasOtherOrganization(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	k.setVar(api, "KEY", "v")
	k.clean(env)
	b := canvasMember(canvasOther)
	k.project(canvasOther, "Theirs")

	if nodes, err := k.app.ListNodes(k.ctx, b, env); err != nil || len(nodes) != 0 {
		t.Errorf("list nodes: %d %v", len(nodes), err)
	}
	if vars, err := k.app.ListVariables(k.ctx, b, api); err != nil || len(vars) != 0 {
		t.Errorf("list variables: %v %v", vars, err)
	}
	if src, err := k.app.ReferenceableVariables(k.ctx, b, api); err != nil || len(src.Sources) != 0 {
		t.Errorf("referenceable: %v %v", src, err)
	}
	if list, _ := k.app.ListProjects(k.ctx, b); len(list) != 1 || list[0].Project.Slug != "theirs" {
		t.Errorf("projects %+v", list)
	}
	if home, _ := k.app.ProjectBySlug(k.ctx, b, "acme"); home != nil {
		t.Errorf("by slug %+v", home)
	}
	_, err := k.app.CreateNode(k.ctx, b, env, app.CreateNodeInput{Type: domain.NodeService})
	canvasWantErr(t, err, domain.CodeProjectNotFound, "Environment not found")
	canvasWantErr(t, k.app.UpdateNode(k.ctx, b, api, app.NodeUpdate{Name: new("mine")}), domain.CodeServiceNotFound, "Node not found")
	canvasWantErr(t, k.app.MoveNode(k.ctx, b, api, domain.Position{X: 1}), domain.CodeServiceNotFound, "Node not found")
	_, err = k.app.DuplicateNode(k.ctx, b, api)
	canvasWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
	_, err = k.app.StartNode(k.ctx, b, api)
	canvasWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
	_, _, err = k.app.StopNode(k.ctx, b, api)
	canvasWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
	canvasWantErr(t, k.app.SetVariable(k.ctx, b, api, app.SetVariableInput{Key: "KEY", Value: "x"}), domain.CodeServiceNotFound, "Node not found")
	canvasWantErr(t, k.app.RemoveVariable(k.ctx, b, api, "KEY"), domain.CodeServiceNotFound, "Node not found")
	if err := k.app.RemoveNode(k.ctx, b, api); err != nil {
		t.Errorf("foreign delete: %v", err)
	}
	n := k.node(api)
	if n.Name != "api" || n.Position != (domain.Position{}) || n.Dirty || k.vars(api)[0].Value != "v" || len(k.ships) != 0 || len(k.removed) != 0 {
		t.Errorf("foreign writes landed: %+v %+v", n, k.vars(api))
	}
	if nodes, _ := k.app.ListNodes(k.ctx, domain.Actor{UserID: "u"}, env); len(nodes) != 0 {
		t.Error("no-org user lists nodes")
	}
}
