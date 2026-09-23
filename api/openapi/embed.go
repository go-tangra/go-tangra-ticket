// Package openapi embeds the ticket browser API contract.
package openapi

import _ "embed"

// Ticket is the OpenAPI 3.1 document served and validated by the service.
//
//go:embed ticket.yaml
var Ticket []byte
