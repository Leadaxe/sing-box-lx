//go:build with_lx_command

package lxd

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// newLocalClientHarness wires a controller with a client registry behind a
// plain-HTTP httptest server (loopback, so the operator routes are reachable)
// and returns a LocalClient pointed at it. clientSecret lets tests present a
// different Bearer than the server expects.
func newLocalClientHarness(t *testing.T, serverSecret, clientSecret string) (*controller, *LocalClient) {
	t.Helper()
	control := newTestController(t, &fakeReloader{}, nil)
	control.clients = newTestRegistry(t)
	control.advertiseAddr = "203.0.113.7:2026"
	control.serverFingerprint = "f0f0cafe"
	server := httptest.NewServer(control.adminHandler(serverSecret))
	t.Cleanup(server.Close)
	return control, NewLocalClient(strings.TrimPrefix(server.URL, "http://"), clientSecret, false)
}

func TestLocalClientMintCodeProducesValidInvite(t *testing.T) {
	control, client := newLocalClientHarness(t, "", "")
	invite, err := client.MintClientCode("label")
	if err != nil {
		t.Fatal(err)
	}

	// Invite is the launcher's wire contract: address#server-fingerprint#code.
	parts := strings.Split(invite, "#")
	if len(parts) != 3 {
		t.Fatal("invite must have 3 #-separated parts, got:", invite)
	}
	if parts[0] != control.advertiseAddr || parts[1] != control.serverFingerprint {
		t.Fatal("invite must carry advertise address and server fingerprint:", invite)
	}

	// The minted code is live: enrolling with it succeeds, and the operator's
	// label from the mint wins over the name the enrollee suggests.
	certDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := control.clients.enroll(parts[2], "self-chosen", certDER)
	if err != nil {
		t.Fatal("minted code must be redeemable:", err)
	}
	if enrolled.Name != "label" {
		t.Fatal("operator label must win over enrollee name, got:", enrolled.Name)
	}
}

func TestLocalClientListClients(t *testing.T) {
	control, client := newLocalClientHarness(t, "", "")

	empty, err := client.ListClients()
	if err != nil {
		t.Fatal(err)
	}
	if empty != "no clients registered" {
		t.Fatal("empty registry must render the placeholder, got:", empty)
	}

	certDER, _ := decodeCertPEM(testClientCertPEM(t))
	code, err := control.clients.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := control.clients.enroll(code, "laptop", certDER)
	if err != nil {
		t.Fatal(err)
	}

	listing, err := client.ListClients()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listing, "laptop") {
		t.Fatal("listing must contain the client name, got:", listing)
	}
	if !strings.Contains(listing, enrolled.Fingerprint[:16]) {
		t.Fatal("listing must contain the truncated fingerprint, got:", listing)
	}
	if strings.Contains(listing, enrolled.Fingerprint) {
		t.Fatal("listing must truncate the fingerprint, got:", listing)
	}
}

func TestLocalClientRemoveClient(t *testing.T) {
	control, client := newLocalClientHarness(t, "", "")
	certDER, _ := decodeCertPEM(testClientCertPEM(t))
	code, err := control.clients.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := control.clients.enroll(code, "gone-soon", certDER)
	if err != nil {
		t.Fatal(err)
	}

	err = client.RemoveClient("no-such-client")
	if err == nil || !strings.Contains(err.Error(), "no such client") {
		t.Fatal("removing an unknown client must fail with 'no such client', got:", err)
	}

	if err = client.RemoveClient("gone-soon"); err != nil {
		t.Fatal("removing an enrolled client failed:", err)
	}
	if control.clients.isTrusted(enrolled.Fingerprint) {
		t.Fatal("removed client must not stay trusted")
	}
}

func TestLocalClientWrongBearerRejected(t *testing.T) {
	_, client := newLocalClientHarness(t, "s", "wrong")
	_, err := client.MintClientCode("label")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("wrong bearer must surface a 401 error, got:", err)
	}
}
