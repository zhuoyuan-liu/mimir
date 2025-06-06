package webhook

// FullValidConfigForTesting is a string representation of a JSON object that contains all fields supported by the notifier Config. It can be used without secrets.
const FullValidConfigForTesting = `{
	"url": "http://localhost",
	"httpMethod": "test-httpMethod",
	"maxAlerts": "2",
	"authorization_scheme": "basic",
	"authorization_credentials": "",
	"username": "test-user",
	"password": "test-pass",
	"title": "test-title",
	"message": "test-message",
	"tlsConfig": {
		"insecureSkipVerify": false,
		"clientCertificate": "test-client-certificate",
		"clientKey": "test-client-key",
		"caCertificate": "test-ca-certificate"
	},
	"hmacConfig": {
		"secret": "test-hmac-secret",
		"header": "X-Grafana-Alerting-Signature",
		"timestampHeader": "X-Grafana-Alerting-Timestamp"
	}
}`

// FullValidSecretsForTesting is a string representation of JSON object that contains all fields that can be overridden from secrets
const FullValidSecretsForTesting = `{
	"username": "test-secret-user",
	"password": "test-secret-pass",
	"tlsConfig.clientCertificate": "test-override-client-certificate",
	"tlsConfig.clientKey": "test-override-client-key",
	"tlsConfig.caCertificate": "test-override-ca-certificate",
	"hmacConfig.secret": "test-override-hmac-secret"
}`
