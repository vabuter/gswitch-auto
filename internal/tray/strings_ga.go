package tray

// Message IDs of the gswitch-auto fork. They carry the i18n.ForkKeyPrefix
// ("strGA") and are translated in the en and ru catalogs only; every other
// locale falls back to English.

// Settings window: tabs and the automatic correction frame.
const (
	strGATabGeneral               = "strGATabGeneral"
	strGATabAuto                  = "strGATabAuto"
	strGASectionAuto              = "strGASectionAuto"
	strGACheckEnabled             = "strGACheckEnabled"
	strGAStatusActive             = "strGAStatusActive"
	strGAStatusOff                = "strGAStatusOff"
	strGAStatusDaemonDown         = "strGAStatusDaemonDown"
	strGAStatusNoIPC              = "strGAStatusNoIPC"
	strGAStatusUnavailable        = "strGAStatusUnavailable" // %s: reason
	strGAReasonDegraded           = "strGAReasonDegraded"
	strGAReasonNoLayoutConverter  = "strGAReasonNoLayoutConverter"
	strGAReasonUnsupportedLayouts = "strGAReasonUnsupportedLayouts"
	strGAReasonLayoutSourceOff    = "strGAReasonLayoutSourceOff"
	strGAReasonLayoutUnknown      = "strGAReasonLayoutUnknown"
	strGAReasonRootMode           = "strGAReasonRootMode"
	strGAReasonDetectorError      = "strGAReasonDetectorError"
	strGALabelMinLetters          = "strGALabelMinLetters"
	strGACheckShortWords          = "strGACheckShortWords"
	strGACheckLearnOnUndo         = "strGACheckLearnOnUndo"
	strGACheckSkipTerminal        = "strGACheckSkipTerminal"
	strGACheckSkipPassword        = "strGACheckSkipPassword"
	strGACheckRequireFocus        = "strGACheckRequireFocus"
	strGAHintTrigger              = "strGAHintTrigger"
	strGAHintNotPersisted         = "strGAHintNotPersisted"
	strGALastCorrection           = "strGALastCorrection" // %s → %s
	strGALastCorrectionNone       = "strGALastCorrectionNone"
	strGAConfigWarnings           = "strGAConfigWarnings"    // %s: joined warnings
	strGAConfigDirMismatch        = "strGAConfigDirMismatch" // %s daemon dir, %s tray dir
)

// Settings window: hotkeys frame and the chord key picker.
const (
	strGASectionHotkeys          = "strGASectionHotkeys"
	strGALabelToggleChord        = "strGALabelToggleChord"
	strGALabelSelectionChord     = "strGALabelSelectionChord"
	strGAPresetDefault           = "strGAPresetDefault" // %s: default chord
	strGAPresetDisabled          = "strGAPresetDisabled"
	strGAPresetCustom            = "strGAPresetCustom"
	strGAHintChords              = "strGAHintChords"
	strGAChordOrderWarning       = "strGAChordOrderWarning" // %s: offending press order
	strGAChordConflictSwitch     = "strGAChordConflictSwitch"
	strGAChordOverlap            = "strGAChordOverlap"
	strGAChordConvertKey         = "strGAChordConvertKey"
	strGAKeyPickerTitleToggle    = "strGAKeyPickerTitleToggle"
	strGAKeyPickerTitleSelection = "strGAKeyPickerTitleSelection"
	strGAKeyPickerHintChord      = "strGAKeyPickerHintChord"
	strGAKeyPickerTooShort       = "strGAKeyPickerTooShort"
	strGAKeyPickerNeedsModifier  = "strGAKeyPickerNeedsModifier"
	strGAKeyPickerTooLong        = "strGAKeyPickerTooLong"
	strGAKeyPickerTextKey        = "strGAKeyPickerTextKey"
)

// Settings window: exception lists and the focus extension.
const (
	strGASectionWords          = "strGASectionWords"
	strGAWordsHint             = "strGAWordsHint"
	strGASectionApps           = "strGASectionApps"
	strGAAppsHint              = "strGAAppsHint"
	strGAButtonAdd             = "strGAButtonAdd"
	strGAButtonRemove          = "strGAButtonRemove"
	strGAButtonAddRunning      = "strGAButtonAddRunning"
	strGAButtonAddLast         = "strGAButtonAddLast"
	strGARunningAppsTitle      = "strGARunningAppsTitle"
	strGARunningAppsError      = "strGARunningAppsError" // %s: error
	strGAColumnApp             = "strGAColumnApp"
	strGAColumnID              = "strGAColumnID"
	strGAInvalidEntry          = "strGAInvalidEntry" // %s: entry
	strGAExtActive             = "strGAExtActive"
	strGAExtDisabled           = "strGAExtDisabled"
	strGAExtNeedsRelogin       = "strGAExtNeedsRelogin"
	strGAExtNotInstalled       = "strGAExtNotInstalled"
	strGAExtError              = "strGAExtError"
	strGAExtOutOfDate          = "strGAExtOutOfDate"
	strGAExtNotGnome           = "strGAExtNotGnome"
	strGAButtonEnableExtension = "strGAButtonEnableExtension"
	strGAButtonEnableNextLogin = "strGAButtonEnableNextLogin"
	strGAReloginDialog         = "strGAReloginDialog"
	strGAErrorEnableExtension  = "strGAErrorEnableExtension"
	strGAErrorSave             = "strGAErrorSave"
	strGAErrorApply            = "strGAErrorApply" // %s: daemon error
	strGAErrorToggle           = "strGAErrorToggle"
	strGAErrorPersist          = "strGAErrorPersist" // %s: write error
)

// Tray menu and tooltip.
const (
	strGAMenuAuto        = "strGAMenuAuto"
	strGAMenuOn          = "strGAMenuOn"
	strGAMenuOff         = "strGAMenuOff"
	strGAMenuUnavailable = "strGAMenuUnavailable"
	strGAMenuHint        = "strGAMenuHint" // %s: toggle chord
	strGAMenuNoHotkey    = "strGAMenuNoHotkey"
	strGATooltipOn       = "strGATooltipOn"
	strGATooltipOff      = "strGATooltipOff"
)
