//go:build with_utls && with_lx_utls_custom

package tls

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	utls "github.com/metacubex/utls"
)

func parseLXCustomClientHello(data []byte) (*utls.ClientHelloSpec, error) {
	var fields struct {
		CipherSuites       []json.RawMessage `json:"cipher_suites"`
		CompressionMethods []json.RawMessage `json:"compression_methods"`
		Extensions         []json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if len(fields.CipherSuites) == 0 || len(fields.CompressionMethods) == 0 || len(fields.Extensions) == 0 {
		return nil, errors.New("cipher_suites, compression_methods and extensions must be non-empty")
	}
	var genericExtensions map[int]*utls.GenericExtension
	for index, extension := range fields.Extensions {
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(extension, &named); err != nil || named.Name == "" {
			return nil, errors.New("each extension must have a name")
		}
		if named.Name == "generic" {
			var raw struct {
				ID         *uint16 `json:"id"`
				DataBase64 *string `json:"data_base64"`
			}
			if err := json.Unmarshal(extension, &raw); err != nil {
				return nil, err
			}
			if raw.ID == nil || raw.DataBase64 == nil {
				return nil, errors.New("generic extension requires id and data_base64")
			}
			payload, err := base64.StdEncoding.DecodeString(*raw.DataBase64)
			if err != nil {
				return nil, err
			}
			if len(payload) > 65535 {
				return nil, errors.New("generic extension data exceeds 65535 bytes")
			}
			if genericExtensions == nil {
				genericExtensions = make(map[int]*utls.GenericExtension)
			}
			genericExtensions[index] = &utls.GenericExtension{Id: *raw.ID, Data: payload}
			// The uTLS JSON parser only accepts named extensions. Replace the generic
			// entry for parsing, then restore its numeric ID and raw payload below.
			fields.Extensions[index] = json.RawMessage(`{"name":"server_name"}`)
		}
	}
	if len(genericExtensions) > 0 {
		var root map[string]json.RawMessage
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(fields.Extensions)
		if err != nil {
			return nil, err
		}
		root["extensions"] = encoded
		data, err = json.Marshal(root)
		if err != nil {
			return nil, err
		}
	}
	var spec utls.ClientHelloSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	for index, extension := range genericExtensions {
		spec.Extensions[index] = extension
	}
	return &spec, nil
}

func validateLXCustomClientHello(data []byte) error {
	_, err := parseLXCustomClientHello(data)
	return err
}

func applyLXCustomClientHello(conn *utls.UConn, data []byte) error {
	spec, err := parseLXCustomClientHello(data)
	if err != nil {
		return err
	}
	return conn.ApplyPreset(spec)
}
