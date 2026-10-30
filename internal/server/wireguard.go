package server

import (
	"context"

	"jailops.net/jailops/sdk/api"
)

// wireguardRoutes serves /api/v1/wireguard. The answer of a peer enrollment carries the peer's join token once; it
// is never stored, logged or audited (the audit entry names the object only).
func (s *Server) wireguardRoutes() []route {
	svc := s.svc
	out := resource[api.WireGuard]{base: "/api/v1/wireguard", param: "name",
		list:   svc.ListWireGuard,
		get:    func(ctx context.Context, k string) (api.WireGuard, error) { return svc.GetWireGuard(ctx, k) },
		create: svc.CreateWireGuard, put: svc.UpdateWireGuard, patch: svc.PatchWireGuard, del: svc.DeleteWireGuard,
		gen: func(v api.WireGuard) int64 { return v.Metadata.Generation }, name: func(v api.WireGuard) string { return v.Metadata.Name },
	}.routes()
	sync := func(c *rc, v api.WireGuard, err error) error {
		if err != nil {
			return err
		}
		c.sync(200, v, v.Metadata.Generation)
		return nil
	}
	return append(out,
		route{method: "POST", pattern: "/api/v1/wireguard/join", h: func(c *rc) error {
			var q api.WireGuardJoin
			if err := c.decode(&q); err != nil {
				return err
			}
			c.object = "/api/v1/wireguard/" + q.Name
			v, err := svc.JoinWireGuard(c.r.Context(), q)
			if err != nil {
				return err
			}
			c.object = "/api/v1/wireguard/" + v.Metadata.Name
			c.w.Header().Set("Location", c.object)
			c.sync(201, v, v.Metadata.Generation)
			return nil
		}},
		route{method: "POST", pattern: "/api/v1/wireguard/{name}/peers", h: func(c *rc) error {
			var q api.WireGuardPeerAdd
			if err := c.decode(&q); err != nil {
				return err
			}
			c.object = "/api/v1/wireguard/" + c.pathArg("name") + "/peers/" + q.Name
			v, err := svc.AddWireGuardPeer(c.r.Context(), c.pathArg("name"), q)
			if err != nil {
				return err
			}
			c.sync(201, v, 0)
			return nil
		}},
		route{method: "DELETE", pattern: "/api/v1/wireguard/{name}/peers/{peer}", h: func(c *rc) error {
			c.object = "/api/v1/wireguard/" + c.pathArg("name") + "/peers/" + c.pathArg("peer")
			v, err := svc.RemoveWireGuardPeer(c.r.Context(), c.pathArg("name"), c.pathArg("peer"))
			return sync(c, v, err)
		}},
		route{method: "POST", pattern: "/api/v1/wireguard/{name}/up", h: func(c *rc) error {
			v, err := svc.SetWireGuardDown(c.r.Context(), c.pathArg("name"), false)
			return sync(c, v, err)
		}},
		route{method: "POST", pattern: "/api/v1/wireguard/{name}/down", h: func(c *rc) error {
			v, err := svc.SetWireGuardDown(c.r.Context(), c.pathArg("name"), true)
			return sync(c, v, err)
		}},
		route{method: "POST", pattern: "/api/v1/wireguard/{name}/rotate-keys", h: func(c *rc) error {
			v, err := svc.RotateWireGuardKeys(c.r.Context(), c.pathArg("name"))
			if err != nil {
				return err
			}
			c.sync(200, v, 0)
			return nil
		}},
	)
}
