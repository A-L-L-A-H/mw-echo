// Package storage exposes the shared Google Drive client through type aliases.
// The implementation lives in github.com/A-L-L-A-H/mw-gdrive so the same
// client can be reused by mw-echo, mw-lab, and any other MoonWitness service.
package storage

import gdrive "github.com/A-L-L-A-H/mw-gdrive"

type GDriveConfig = gdrive.GDriveConfig
type GDriveQuota = gdrive.GDriveQuota
type GDriveClient = gdrive.GDriveClient

var (
	NewGDriveClient       = gdrive.NewGDriveClient
	BuildGoogleAuthURL    = gdrive.BuildGoogleAuthURL
	ExchangeGoogleAuthCode = gdrive.ExchangeGoogleAuthCode
)
