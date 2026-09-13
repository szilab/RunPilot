package model

import "fmt"

func xpraBool(value bool) *bool { return &value }

// DefaultXpraRemoteOptions is the single canonical RunPilot profile for
// browser-based administration. Keep frontend presentation derived from this
// normalized API configuration rather than duplicating these values there.
func DefaultXpraRemoteOptions() XpraRemoteOptions {
	return XpraRemoteOptions{
		Profile:            XpraProfileRecommended,
		Encoding:           "webp",
		Video:              xpraBool(false),
		DPIMode:            XpraDPIAuto,
		LaunchAfterConnect: xpraBool(true),
		Clipboard:          xpraBool(true),
		DynamicResize:      xpraBool(true),
		Menu:               XpraMenuAutohide,
		ToolbarPosition:    "top-left",
		Sound:              xpraBool(false),
		Printing:           xpraBool(false),
		FileTransfer:       xpraBool(false),
	}
}

// NormalizeXpraRemoteOptions applies defaults to partial and legacy settings,
// validates values supplied through the API, and makes preset semantics
// deterministic. A manual encoding/video override always becomes Custom.
func NormalizeXpraRemoteOptions(input *XpraRemoteOptions) (XpraRemoteOptions, error) {
	out := DefaultXpraRemoteOptions()
	if input == nil {
		return out, nil
	}
	if input.Sound != nil && *input.Sound {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra sound is disabled by the RunPilot Remote profile")
	}
	if input.Printing != nil && *input.Printing {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra printing is disabled by the RunPilot Remote profile")
	}
	if input.FileTransfer != nil && *input.FileTransfer {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra file transfer is disabled by the RunPilot Remote profile")
	}
	if input.Profile != "" {
		out.Profile = input.Profile
	}
	if input.Encoding != "" {
		out.Encoding = input.Encoding
	}
	if input.Video != nil {
		out.Video = xpraBool(*input.Video)
	}
	if input.DPIMode != "" {
		out.DPIMode = input.DPIMode
	}
	if input.DPI != 0 {
		out.DPI = input.DPI
	}
	if input.LaunchAfterConnect != nil {
		out.LaunchAfterConnect = xpraBool(*input.LaunchAfterConnect)
	}
	if input.Clipboard != nil {
		out.Clipboard = xpraBool(*input.Clipboard)
	}
	if input.DynamicResize != nil {
		out.DynamicResize = xpraBool(*input.DynamicResize)
	}
	if input.Menu != "" {
		out.Menu = input.Menu
	}
	if input.ToolbarPosition != "" {
		out.ToolbarPosition = input.ToolbarPosition
	}
	if input.Sound != nil {
		out.Sound = xpraBool(*input.Sound)
	}
	if input.Printing != nil {
		out.Printing = xpraBool(*input.Printing)
	}
	if input.FileTransfer != nil {
		out.FileTransfer = xpraBool(*input.FileTransfer)
	}

	if !validXpraProfile(out.Profile) {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra rendering profile must be recommended, automatic, compatibility, or custom")
	}
	if !validXpraEncoding(out.Encoding) {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra picture encoding must be auto, webp, or rgb")
	}
	if !validXpraDPIMode(out.DPIMode) {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra DPI mode must be auto, 96, or custom")
	}
	if !validXpraMenu(out.Menu) {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra menu mode must be auto-hide, visible, or hidden")
	}
	if out.ToolbarPosition != "top-left" && out.ToolbarPosition != "top-right" {
		return XpraRemoteOptions{}, fmt.Errorf("Xpra toolbar position must be top-left or top-right")
	}
	switch out.DPIMode {
	case XpraDPIAuto:
		out.DPI = 0
	case XpraDPI96:
		out.DPI = 96
	case XpraDPICustom:
		if out.DPI < 72 || out.DPI > 240 {
			return XpraRemoteOptions{}, fmt.Errorf("custom Xpra DPI must be between 72 and 240")
		}
	}

	// A selected preset wins only while its rendering values are unchanged.
	// This avoids invisible stale overrides after a manual form edit.
	if out.Profile != XpraProfileCustom && ((input.Encoding != "" && out.Encoding != presetEncoding(out.Profile)) || (input.Video != nil && *out.Video != presetVideo(out.Profile))) {
		out.Profile = XpraProfileCustom
	}
	if out.Profile != XpraProfileCustom {
		out.Encoding = presetEncoding(out.Profile)
		out.Video = xpraBool(presetVideo(out.Profile))
	}
	return out, nil
}

func validXpraProfile(value XpraProfile) bool {
	return value == XpraProfileRecommended || value == XpraProfileAutomatic || value == XpraProfileCompatibility || value == XpraProfileCustom
}
func validXpraEncoding(value string) bool {
	return value == "auto" || value == "webp" || value == "rgb"
}
func validXpraDPIMode(value XpraDPIMode) bool {
	return value == XpraDPIAuto || value == XpraDPI96 || value == XpraDPICustom
}
func validXpraMenu(value XpraMenuMode) bool {
	return value == XpraMenuAutohide || value == XpraMenuVisible || value == XpraMenuHidden
}
func presetEncoding(profile XpraProfile) string {
	if profile == XpraProfileCompatibility {
		return "rgb"
	}
	if profile == XpraProfileAutomatic {
		return "auto"
	}
	return "webp"
}
func presetVideo(profile XpraProfile) bool { return profile == XpraProfileAutomatic }
