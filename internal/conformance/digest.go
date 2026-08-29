package conformance

import (
	"crypto/sha256"
	"encoding/hex"
)

func FixtureDigest(bytes []byte) string {
	return domainHash("prism:fixture:v1", bytes)
}

func ScenarioManifestDigest(expanded Scenario) string {
	return domainHash("prism:scenario-manifest:v1", jcsOf(expanded))
}

func SuiteManifestDigest(expanded SuiteManifest) string {
	return domainHash("prism:suite-manifest:v1", jcsOf(expanded))
}

func jcsOf(v any) []byte {
	out, err := JCS(decodeJSON(mustJSON(v)))
	if err != nil {
		panic(err)
	}
	return out
}

func domainHash(domain string, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
