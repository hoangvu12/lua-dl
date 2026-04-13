// Package steam is a thin shim over Lucino772/envelop's steamcm stack.
//
// We bypass envelop's higher-level SteamDownloadClient because it:
//   - calls accountHasAccess(), which queries the anonymous account's license
//     package (17906) and rejects paid apps — we don't download via Steam CDN
//     anyway, so the access check is irrelevant.
//   - calls DownloadManifest() via Steam CDN with a manifest request code, which
//     is AccessDenied for anonymous on paid apps. Our manifests come from ryuu
//     and GH mirrors as raw bytes, fed directly to steamcdn.NewDepotManifest.
package steam

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Lucino772/envelop/pkg/steam/steamcm"
	"github.com/Lucino772/envelop/pkg/steam/steampb"
	"github.com/Lucino772/envelop/pkg/steam/steamvdf"
)

type Depot struct {
	DepotID    uint32
	ManifestID uint64
	Name       string
	MaxSize    uint64
}

type AppInfo struct {
	AppID  uint32
	Name   string
	Depots []Depot
}

type Client struct {
	conn    *steamcm.SteamConnection
	user    *steamcm.SteamUserHandler
	apps    *steamcm.SteamAppsHandler
	content *steamcm.SteamContentHandler
}

func NewClient() *Client {
	user := steamcm.NewUserHandler()
	apps := steamcm.NewAppsHandler()
	unified := steamcm.NewSteamUnifiedMessageHandler()
	content := steamcm.NewSteamContentHandler(unified)
	conn := steamcm.NewSteamConnection(
		steamcm.NewSteamBaseHandler(),
		user,
		apps,
		unified,
		content,
	)
	return &Client{conn: conn, user: user, apps: apps, content: content}
}

// GetServersForSteamPipe returns the CDN content-server list for a cell.
// Use cellID=0 if unknown — Steam will still return a usable list.
func (c *Client) GetServersForSteamPipe(cellID uint32) ([]*steampb.CContentServerDirectory_ServerInfo, error) {
	return c.content.GetServersForSteamPipe(c.conn, cellID)
}

// GetCDNAuthToken fetches a per-(app,depot,server) CDN auth token. Tokens
// are short-lived and optional — empty string is valid when the server
// doesn't require auth.
func (c *Client) GetCDNAuthToken(appID, depotID uint32, serverHost string) (string, error) {
	return c.content.GetCDNAuthToken(c.conn, appID, depotID, serverHost)
}

// Connect dials a Steam CM and waits for the encrypted channel.
//
// envelop's SteamConnection.Connect() has a latent nil-pointer bug: if the
// CM directory fetch returns an empty list (transient DNS or API hiccup),
// Servers.PickServer() returns nil and Dial dereferences nil. We wrap the
// call to convert that panic into a normal error and retry a few times
// before giving up.
func (c *Client) Connect(timeout time.Duration) error {
	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := c.safeConnect(); err != nil {
			lastErr = err
			time.Sleep(time.Duration(500*(i+1)) * time.Millisecond)
			continue
		}
		return c.conn.WaitReady(timeout)
	}
	return fmt.Errorf("steam connect failed after %d attempts: %w", attempts, lastErr)
}

func (c *Client) safeConnect() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("envelop panicked (likely empty CM directory): %v", r)
		}
	}()
	return c.conn.Connect()
}

// LogInAnonymously performs an anonymous logon. No credentials.
func (c *Client) LogInAnonymously() error {
	resp, err := c.user.LogInAnonymously(c.conn)
	if err != nil {
		return err
	}
	// EResult_OK is 1. We don't import steamlang here to keep the shim tight.
	if resp.GetEresult() != 1 {
		return errors.New("anonymous logon failed: eresult=" +
			strconv.Itoa(int(resp.GetEresult())))
	}
	return nil
}

// GetAppInfo fetches PICS ProductInfo for a single app and extracts the
// common.name plus each depot's public manifest gid. Anonymous accounts can
// always read public PICS data — no access check needed.
func (c *Client) GetAppInfo(appID uint32) (*AppInfo, error) {
	resp, err := c.apps.PICSGetProductInfo(
		c.conn,
		[]steamcm.PICSRequest{{ID: appID, AccessToken: 0}},
		nil,
		false,
	)
	if err != nil {
		return nil, err
	}
	if len(resp.Apps) == 0 {
		return nil, errors.New("PICS returned no apps")
	}
	app := findApp(resp.Apps, appID)
	if app == nil {
		return nil, errors.New("PICS response missing requested app")
	}

	kv, err := steamvdf.ReadBytes(app.GetBuffer())
	if err != nil {
		return nil, err
	}

	info := &AppInfo{AppID: app.GetAppid()}
	if common, ok := kv.GetChild("common"); ok {
		if name, ok := common.GetChild("name"); ok {
			info.Name = name.Value
		}
	}
	if info.Name == "" {
		info.Name = "app-" + strconv.FormatUint(uint64(appID), 10)
	}

	if depots, ok := kv.GetChild("depots"); ok {
		for _, child := range depots.Children {
			id, err := strconv.ParseUint(child.Key, 10, 32)
			if err != nil {
				continue // skip non-numeric keys like "branches", "baselanguages"
			}
			d := Depot{DepotID: uint32(id)}
			if name, ok := child.GetChild("name"); ok {
				d.Name = name.Value
			}
			if maxsize, ok := child.GetChild("maxsize"); ok {
				if n, err := strconv.ParseUint(maxsize.Value, 10, 64); err == nil {
					d.MaxSize = n
				}
			}
			if manifests, ok := child.GetChild("manifests"); ok {
				if pub, ok := manifests.GetChild("public"); ok {
					if gid, ok := pub.GetChild("gid"); ok {
						if n, err := strconv.ParseUint(gid.Value, 10, 64); err == nil {
							d.ManifestID = n
						}
					}
				}
			}
			info.Depots = append(info.Depots, d)
		}
	}
	return info, nil
}

func findApp(apps []*steampb.CMsgClientPICSProductInfoResponse_AppInfo, id uint32) *steampb.CMsgClientPICSProductInfoResponse_AppInfo {
	for _, a := range apps {
		if a.GetAppid() == id {
			return a
		}
	}
	return nil
}
