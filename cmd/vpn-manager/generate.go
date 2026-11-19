package main

//go:generate go -C ../.. run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1 -config api/oapi.yaml api/openapi.json
//go:generate go -C ../.. run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
