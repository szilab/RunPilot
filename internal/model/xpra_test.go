package model

import "testing"

func xpraTestBool(value bool) *bool { return &value }

func TestNormalizeXpraRemoteOptionsDefaultsAndPresets(t *testing.T) {
	defaults, err := NormalizeXpraRemoteOptions(nil)
	if err != nil || defaults.Profile != XpraProfileRecommended || defaults.Encoding != "webp" || *defaults.Video || defaults.DPIMode != XpraDPIAuto || !*defaults.LaunchAfterConnect || !*defaults.Clipboard || *defaults.Sound || *defaults.Printing || *defaults.FileTransfer {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	for _, test := range []struct {
		profile  XpraProfile
		encoding string
		video    bool
	}{
		{XpraProfileRecommended, "webp", false},
		{XpraProfileAutomatic, "auto", true},
		{XpraProfileCompatibility, "rgb", false},
	} {
		got, err := NormalizeXpraRemoteOptions(&XpraRemoteOptions{Profile: test.profile})
		if err != nil || got.Encoding != test.encoding || *got.Video != test.video {
			t.Fatalf("%s = %#v, %v", test.profile, got, err)
		}
	}
}

func TestNormalizeXpraRemoteOptionsManualRenderingIsCustom(t *testing.T) {
	got, err := NormalizeXpraRemoteOptions(&XpraRemoteOptions{Profile: XpraProfileRecommended, Encoding: "rgb", Video: xpraTestBool(false)})
	if err != nil || got.Profile != XpraProfileCustom || got.Encoding != "rgb" || *got.Video {
		t.Fatalf("manual settings = %#v, %v", got, err)
	}
}

func TestNormalizeXpraRemoteOptionsRejectsInvalidValues(t *testing.T) {
	for _, options := range []*XpraRemoteOptions{
		{Encoding: "h264"}, {DPIMode: XpraDPICustom, DPI: 300}, {DPIMode: "bogus"}, {Menu: "bottom"}, {ToolbarPosition: "left"}, {Sound: xpraTestBool(true)}, {Printing: xpraTestBool(true)}, {FileTransfer: xpraTestBool(true)},
	} {
		if _, err := NormalizeXpraRemoteOptions(options); err == nil {
			t.Fatalf("invalid options accepted: %#v", options)
		}
	}
}

func TestNormalizeXpraRemoteOptionsDPI(t *testing.T) {
	for _, test := range []struct {
		input XpraRemoteOptions
		mode  XpraDPIMode
		dpi   int
	}{
		{XpraRemoteOptions{}, XpraDPIAuto, 0},
		{XpraRemoteOptions{DPIMode: XpraDPI96}, XpraDPI96, 96},
		{XpraRemoteOptions{DPIMode: XpraDPICustom, DPI: 144}, XpraDPICustom, 144},
	} {
		got, err := NormalizeXpraRemoteOptions(&test.input)
		if err != nil || got.DPIMode != test.mode || got.DPI != test.dpi {
			t.Fatalf("DPI %#v = %#v, %v", test.input, got, err)
		}
	}
}
