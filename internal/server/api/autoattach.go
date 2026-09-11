package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Alia5/VIIPER/usbip"
)

func AttachLocalhostClient(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, useNativeIOCTL bool, logger *slog.Logger) error {
	_, err := AttachLocalhostClientWithPort(ctx, deviceExportMeta, usbipServerPort, useNativeIOCTL, logger)
	return err
}

func AttachLocalhostClientWithPort(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, useNativeIOCTL bool, logger *slog.Logger) (int, error) {
	return attachLocalhostClientImpl(ctx, deviceExportMeta, usbipServerPort, useNativeIOCTL, logger)
}

func DetachLocalhostClient(ctx context.Context, port int, logger *slog.Logger) error {
	return detachLocalhostClientImpl(ctx, port, logger)
}

func parseAttachedPort(output []byte) (int, error) {
	text := strings.TrimSpace(string(output))
	if port, err := strconv.Atoi(text); err == nil && port > 0 {
		return port, nil
	}
	fields := strings.Fields(text)
	for i := len(fields) - 1; i >= 0; i-- {
		field := strings.Trim(fields[i], ".:,\r\n")
		if port, err := strconv.Atoi(field); err == nil && port > 0 {
			return port, nil
		}
	}
	return 0, fmt.Errorf("invalid usbip attach output %q", text)
}
