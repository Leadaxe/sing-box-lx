//go:build with_lxd

package lxd

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// LocalClient talks to a running daemon's admin plane over loopback for
// the `client` subcommands. It skips TLS verification (loopback, same host)
// but still needs to reach the mTLS listener — so it presents no client cert
// and relies on the enroll/clients routes being reachable, plus the Bearer
// secret when set. For `client add`/`list`/`remove` we hit admin routes that
// require trust; on the same host the operator is assumed authorized, so these
// commands are served on a loopback-only, cert-exempt path (see adminHandler
// note). For now they require --secret if the daemon was started with one.
type LocalClient struct {
	base   string
	secret string
	http   *http.Client
}

func NewLocalClient(listen, secret string, useTLS bool) *LocalClient {
	scheme := "http"
	transport := &http.Client{Timeout: 10 * time.Second}
	if useTLS {
		scheme = "https"
		transport = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback CLI to local daemon
			},
		}
	}
	return &LocalClient{base: scheme + "://" + listen, secret: secret, http: transport}
}

func (c *LocalClient) do(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if c.secret != "" {
		request.Header.Set("Authorization", "Bearer "+c.secret)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, E.Cause(err, "reach daemon (is it running?)")
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return data, response.StatusCode, nil
}

func (c *LocalClient) MintClientCode(name string) (string, error) {
	data, status, err := c.do(http.MethodPost, "/admin/client-code", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", E.New("daemon returned ", status, ": ", string(data))
	}
	var out struct {
		Invite string `json:"invite"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.Invite, nil
}

func (c *LocalClient) ListClients() (string, error) {
	data, status, err := c.do(http.MethodGet, "/admin/clients", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", E.New("daemon returned ", status, ": ", string(data))
	}
	var out struct {
		Clients []trustedClient `json:"clients"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if len(out.Clients) == 0 {
		return "no clients registered", nil
	}
	result := fmt.Sprintf("%-20s %-18s %s\n", "NAME", "FINGERPRINT", "ADDED")
	for _, client := range out.Clients {
		fp := client.Fingerprint
		if len(fp) > 16 {
			fp = fp[:16]
		}
		result += fmt.Sprintf("%-20s %-18s %s\n", client.Name, fp, client.AddedAt)
	}
	return result, nil
}

func (c *LocalClient) RemoveClient(nameOrFingerprint string) error {
	data, status, err := c.do(http.MethodPost, "/admin/client-remove", map[string]string{"target": nameOrFingerprint})
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return E.New("no such client")
	}
	if status != http.StatusOK {
		return E.New("daemon returned ", status, ": ", string(data))
	}
	return nil
}
