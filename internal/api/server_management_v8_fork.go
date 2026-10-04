package api

import "github.com/gin-gonic/gin"

// registerManagementV8ForkRoutes retains fork operations on the authenticated
// v8 group. Configuration writes inherit the group's v8 migration context.
func (s *Server) registerManagementV8ForkRoutes(v8 *gin.RouterGroup) {
	v8.POST("/provider-connectivity-test", s.mgmt.ProviderConnectivityTest)
	v8.POST("/quota-refresh-jobs", s.mgmt.StartQuotaRefreshJob)
	v8.GET("/quota-refresh-jobs/:id", s.mgmt.GetQuotaRefreshJob)
	v8.GET("/request-logs", s.mgmt.GetRequestLogs)
	v8.GET("/request-logs/export", s.mgmt.ExportRequestLogs)
	v8.GET("/request-logs/failure-details", s.mgmt.GetRequestLogFailureDetails)
	v8.GET("/request-logs/:id", s.mgmt.GetRequestLogDetail)
	v8.GET("/request-log-retention-days", s.mgmt.GetRequestLogRetentionDays)
	v8.PUT("/request-log-retention-days", s.mgmt.PutRequestLogRetentionDays)
	v8.GET("/auth-files/download-zip", s.mgmt.DownloadAuthFilesZip)
	v8.POST("/auth-files/download-zip", s.mgmt.DownloadAuthFilesZip)
	v8.POST("/management-panel/update", s.mgmt.PostManagementPanelUpdate)
	v8.GET("/media-providers", s.mgmt.GetMediaProviders)
	v8.PUT("/media-providers", s.mgmt.PutMediaProviders)
	v8.PATCH("/media-providers", s.mgmt.PatchMediaProviders)
	v8.DELETE("/media-providers", s.mgmt.DeleteMediaProviders)
}
