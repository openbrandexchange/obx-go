package mock

import (
	"net/http"

	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

func (n *Node) routes() {
	m := http.NewServeMux()
	n.mux = m
	const api = obx.PathAPIBase
	const ing = obx.PathIngest
	const adm = obx.PathAdminAPI

	// Protocol.
	m.HandleFunc("GET "+obx.PathDiscovery, n.handleDiscovery)
	m.HandleFunc("OPTIONS "+obx.PathDiscovery, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST "+api+"/access-requests", n.handleCreateAccessRequest)
	m.HandleFunc("GET "+api+"/access-requests/{id}", n.handleGetAccessRequest)
	m.HandleFunc("POST "+api+"/oauth/token", n.handleToken)
	m.HandleFunc("GET "+api+"/grant", n.handleGrant)
	m.HandleFunc("GET "+api+"/changes", n.handleChanges)
	m.HandleFunc("GET "+api+"/brands", n.handleBrands)
	m.HandleFunc("GET "+api+"/brands/{id}", n.handleBrand)
	m.HandleFunc("GET "+api+"/products", n.handleProducts)
	m.HandleFunc("GET "+api+"/products/{id}", n.handleProduct)
	m.HandleFunc("GET "+api+"/products:by-gtin", n.handleByGTIN)
	m.HandleFunc("POST "+api+"/products:by-gtin", n.handleByGTIN)
	m.HandleFunc("GET "+api+"/assets/{id}", n.handleAsset)
	m.HandleFunc("GET "+api+"/assets/{id}/renditions/{name}", n.handleRendition)
	m.HandleFunc("POST "+api+"/exports", n.handleCreateExport)
	m.HandleFunc("GET "+api+"/exports/{id}", n.handleGetExport)
	m.HandleFunc("GET "+api+"/exports/{id}/manifest", n.handleExportManifest)
	m.HandleFunc("GET "+api+"/exports/{id}/parts/{number}", n.handleExportPart)
	m.HandleFunc("POST "+api+"/usage", n.handleUsage)
	m.HandleFunc("GET "+api+"/webhooks", n.handleWebhooks)
	m.HandleFunc("POST "+api+"/webhooks", n.handleWebhooks)
	m.HandleFunc("DELETE "+api+"/webhooks/{id}", n.handleDeleteWebhook)
	m.HandleFunc("POST "+api+"/webhooks/{id}/test", n.handleTestWebhook)

	// Ingest.
	m.HandleFunc("GET "+ing, n.handleIngestInfo)
	m.HandleFunc("GET "+ing+"/{$}", n.handleIngestInfo)
	for _, k := range []string{"brands", "products", "variants", "assets"} {
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE"} {
			m.HandleFunc(method+" "+ing+"/"+k+"/{external_id}", n.handleIngestRecord)
		}
	}
	m.HandleFunc("PUT "+ing+"/products/{external_id}/asset-order", n.handleIngestAssetOrder)
	m.HandleFunc("PUT "+ing+"/assets/{external_id}/files/{rendition}", n.handleIngestAssetFile)
	m.HandleFunc("HEAD "+ing+"/blobs/{sha256}", n.handleIngestBlob)
	m.HandleFunc("PUT "+ing+"/blobs/{sha256}", n.handleIngestBlob)
	m.HandleFunc("POST "+ing+"/uploads", n.handleCreateUpload)
	m.HandleFunc("GET "+ing+"/uploads/{id}", n.handleUpload)
	m.HandleFunc("DELETE "+ing+"/uploads/{id}", n.handleUpload)
	m.HandleFunc("PUT "+ing+"/uploads/{id}/parts/{number}", n.handleUploadPart)
	m.HandleFunc("POST "+ing+"/uploads/{id}/complete", n.handleCompleteUpload)
	m.HandleFunc("POST "+ing+"/batch", n.handleBatch)
	m.HandleFunc("POST "+ing+"/imports", n.handleIngestImport)
	m.HandleFunc("GET "+ing+"/jobs", n.handleJobs)
	m.HandleFunc("GET "+ing+"/jobs/{id}", n.handleJob)
	m.HandleFunc("GET "+ing+"/jobs/{id}/results", n.handleJobResults)

	// Node admin: public and session.
	const admin, anyRole, public = nodeadmin.RoleAdmin, "any", ""
	m.HandleFunc("GET "+adm+"/public/branding", n.handlePublicBranding)
	m.HandleFunc("GET "+adm+"/public/logo", n.handlePublicLogo)
	m.HandleFunc("GET "+adm+"/setup", n.handleGetSetup)
	m.HandleFunc("POST "+adm+"/setup", n.handleSetup)
	m.HandleFunc("GET "+adm+"/session", n.handleSession)
	m.HandleFunc("POST "+adm+"/session/login", n.admin(public, n.handleLogin))
	m.HandleFunc("POST "+adm+"/session/logout", n.admin(public, n.handleLogout))
	m.HandleFunc("POST "+adm+"/password/forgot", n.admin(public, n.handleForgot))
	m.HandleFunc("POST "+adm+"/password/reset", n.admin(public, n.handleReset))
	m.HandleFunc("GET "+adm+"/invites/{token}", n.admin(public, n.handleGetInvite))
	m.HandleFunc("POST "+adm+"/invites/{token}/accept", n.admin(public, n.handleAcceptInvite))
	m.HandleFunc("PATCH "+adm+"/me", n.admin(anyRole, n.handleMe))

	// Settings.
	m.HandleFunc("GET "+adm+"/settings", n.admin(admin, n.handleSettings))
	m.HandleFunc("PATCH "+adm+"/settings", n.admin(admin, n.handleSettings))
	m.HandleFunc("PUT "+adm+"/settings/logo", n.admin(admin, n.handleLogo))
	m.HandleFunc("DELETE "+adm+"/settings/logo", n.admin(admin, n.handleLogo))

	// Catalog.
	m.HandleFunc("GET "+adm+"/catalog/brands", n.admin(admin, n.handleAdminBrands))
	for _, method := range []string{"PUT", "PATCH", "DELETE"} {
		m.HandleFunc(method+" "+adm+"/catalog/brands/{external_id}", n.admin(admin, n.adminRecord(obx.KindBrand)))
		m.HandleFunc(method+" "+adm+"/catalog/variants/{external_id}", n.admin(admin, n.adminRecord(obx.KindVariant)))
		m.HandleFunc(method+" "+adm+"/catalog/assets/{external_id}", n.admin(admin, n.adminRecord(obx.KindAsset)))
		m.HandleFunc(method+" "+adm+"/catalog/products/{external_id}", n.admin(admin, n.handleAdminProduct))
	}
	m.HandleFunc("GET "+adm+"/catalog/products", n.admin(admin, n.handleAdminProducts))
	m.HandleFunc("GET "+adm+"/catalog/products/{external_id}", n.admin(admin, n.handleAdminProduct))
	m.HandleFunc("PUT "+adm+"/catalog/products/{external_id}/asset-order", n.admin(admin, n.handleAdminAssetOrder))
	m.HandleFunc("PUT "+adm+"/catalog/assets/{external_id}/files/{rendition}", n.admin(admin, n.handleAdminAssetFile))
	m.HandleFunc("HEAD "+adm+"/catalog/blobs/{sha256}", n.admin(admin, n.handleAdminBlob))
	m.HandleFunc("PUT "+adm+"/catalog/blobs/{sha256}", n.admin(admin, n.handleAdminBlob))

	// Imports.
	m.HandleFunc("GET "+adm+"/imports", n.admin(admin, n.handleAdminImports))
	m.HandleFunc("POST "+adm+"/imports", n.admin(admin, n.handleAdminImports))
	m.HandleFunc("GET "+adm+"/imports/{id}", n.admin(admin, n.handleAdminImport))
	m.HandleFunc("GET "+adm+"/imports/{id}/errors", n.admin(admin, n.handleAdminImportErrors))
	m.HandleFunc("GET "+adm+"/import-template.csv", n.admin(admin, n.handleImportTemplate))
	m.HandleFunc("GET "+adm+"/import-sources", n.admin(admin, n.handleImportSources))
	m.HandleFunc("POST "+adm+"/import-sources", n.admin(admin, n.handleImportSources))
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		m.HandleFunc(method+" "+adm+"/import-sources/{id}", n.admin(admin, n.handleImportSource))
	}
	m.HandleFunc("POST "+adm+"/import-sources/{id}/run", n.admin(admin, n.handleRunSource))
	m.HandleFunc("POST "+adm+"/import-sources/{id}/test", n.admin(admin, n.handleTestSource))

	// Retailers, access requests, users, keys.
	m.HandleFunc("GET "+adm+"/retailers", n.admin(admin, n.handleRetailers))
	m.HandleFunc("POST "+adm+"/retailers", n.admin(admin, n.handleRetailers))
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		m.HandleFunc(method+" "+adm+"/retailers/{id}", n.admin(admin, n.handleRetailer))
	}
	m.HandleFunc("GET "+adm+"/access-requests", n.admin(admin, n.handleAccessRequests))
	m.HandleFunc("GET "+adm+"/access-requests/{id}", n.admin(admin, n.handleAccessRequest))
	m.HandleFunc("POST "+adm+"/access-requests/{id}/approve", n.admin(admin, n.handleApprove))
	m.HandleFunc("POST "+adm+"/access-requests/{id}/reject", n.admin(admin, n.handleReject))
	m.HandleFunc("GET "+adm+"/users", n.admin(admin, n.handleUsers))
	m.HandleFunc("POST "+adm+"/users/invites", n.admin(admin, n.handleInviteUser))
	m.HandleFunc("PATCH "+adm+"/users/{id}", n.admin(admin, n.handleUser))
	m.HandleFunc("DELETE "+adm+"/users/{id}", n.admin(admin, n.handleUser))
	m.HandleFunc("POST "+adm+"/users/{id}/resend-invite", n.admin(admin, n.handleResendInvite))
	m.HandleFunc("GET "+adm+"/api-clients", n.admin(admin, n.handleAPIClients))
	m.HandleFunc("POST "+adm+"/api-clients", n.admin(admin, n.handleAPIClients))
	m.HandleFunc("POST "+adm+"/api-clients/{id}/rotate", n.admin(admin, n.handleAPIClient))
	m.HandleFunc("DELETE "+adm+"/api-clients/{id}", n.admin(admin, n.handleAPIClient))
	m.HandleFunc("GET "+adm+"/ingest-tokens", n.admin(admin, n.handleIngestTokens))
	m.HandleFunc("POST "+adm+"/ingest-tokens", n.admin(admin, n.handleIngestTokens))
	m.HandleFunc("POST "+adm+"/ingest-tokens/{id}/rotate", n.admin(admin, n.handleIngestToken))
	m.HandleFunc("DELETE "+adm+"/ingest-tokens/{id}", n.admin(admin, n.handleIngestToken))
	m.HandleFunc("GET "+adm+"/webhooks", n.admin(admin, n.handleAdminWebhooks))
	m.HandleFunc("POST "+adm+"/webhooks", n.admin(admin, n.handleAdminWebhooks))
	m.HandleFunc("PATCH "+adm+"/webhooks/{id}", n.admin(admin, n.handleAdminWebhook))
	m.HandleFunc("DELETE "+adm+"/webhooks/{id}", n.admin(admin, n.handleAdminWebhook))
	m.HandleFunc("POST "+adm+"/webhooks/{id}/test", n.handleAdminWebhookTest)
	m.HandleFunc("GET "+adm+"/webhooks/{id}/deliveries", n.admin(admin, n.handleAdminWebhookDeliveries))
	m.HandleFunc("GET "+adm+"/stats", n.admin(admin, n.handleStats))
	m.HandleFunc("GET "+adm+"/stats/events", n.admin(admin, n.handleStatsEvents))

	// Portal.
	m.HandleFunc("GET "+adm+"/portal/viewer", n.admin(anyRole, n.handlePortalViewer))
	m.HandleFunc("GET "+adm+"/portal/products", n.admin(anyRole, n.handlePortalProducts))
	m.HandleFunc("GET "+adm+"/portal/facets", n.admin(anyRole, n.handlePortalFacets))
	m.HandleFunc("GET "+adm+"/portal/products/{id}", n.admin(anyRole, n.handlePortalProduct))
	m.HandleFunc("POST "+adm+"/portal/gtin-lookup", n.admin(anyRole, n.handlePortalGTIN))
	m.HandleFunc("GET "+adm+"/portal/selection", n.admin(anyRole, n.handleSelection))
	m.HandleFunc("PUT "+adm+"/portal/selection", n.admin(anyRole, n.handleSelection))
	m.HandleFunc("DELETE "+adm+"/portal/selection", n.admin(anyRole, n.handleSelection))
	m.HandleFunc("GET "+adm+"/portal/assets/{id}/download", n.admin(anyRole, n.handlePortalAssetDownload))
	m.HandleFunc("GET "+adm+"/portal/products/{id}/download.zip", n.admin(anyRole, n.handlePortalProductZip))
	m.HandleFunc("GET "+adm+"/portal/selection/download.zip", n.admin(anyRole, n.handleSelectionZip))
	m.HandleFunc("GET "+adm+"/portal/selection/data.csv", n.admin(anyRole, n.handleSelectionCSV))
	m.HandleFunc("GET "+adm+"/portal/products/data.csv", n.admin(anyRole, n.handleProductsCSV))
	m.HandleFunc("GET "+adm+"/portal/download-jobs", n.admin(anyRole, n.handleDownloadJobs))
	m.HandleFunc("POST "+adm+"/portal/download-jobs", n.admin(anyRole, n.handleDownloadJobs))
	m.HandleFunc("GET "+adm+"/portal/download-jobs/{id}", n.admin(anyRole, n.handleDownloadJob))
	m.HandleFunc("GET "+adm+"/portal/download-jobs/{id}/file", n.admin(anyRole, n.handleDownloadJobFile))
	m.HandleFunc("GET "+adm+"/portal/api-clients", n.admin(anyRole, n.handlePortalClients))
	m.HandleFunc("POST "+adm+"/portal/api-clients", n.admin(anyRole, n.handlePortalClients))
	m.HandleFunc("POST "+adm+"/portal/api-clients/{id}/rotate", n.admin(anyRole, n.handlePortalClient))
	m.HandleFunc("DELETE "+adm+"/portal/api-clients/{id}", n.admin(anyRole, n.handlePortalClient))

	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><title>OBX mock node</title><p>This is an in-memory Open Brand Exchange mock node. Discovery: <a href="/.well-known/obx">/.well-known/obx</a></p>`))
	})
}
