package main

import "trigger-admin-bot/internal/model"

type Trigger = model.Trigger

type ResponseTextItem = model.ResponseTextItem
type ResponseTemplate = model.ResponseTemplate

type TriggerMode = model.TriggerMode

type AdminMode = model.AdminMode

type MatchType = model.MatchType

type ActionType = model.ActionType

const (
	TriggerModeAll                      = model.TriggerModeAll
	TriggerModeOnlyReplies              = model.TriggerModeOnlyReplies
	TriggerModeOnlyRepliesToBot         = model.TriggerModeOnlyRepliesToBot
	TriggerModeOnlyRepliesToSelf        = model.TriggerModeOnlyRepliesToSelf
	TriggerModeOnlyRepliesToSelfNoMedia = model.TriggerModeOnlyRepliesToSelfNoMedia
	TriggerModeNeverOnReplies           = model.TriggerModeNeverOnReplies
	TriggerModeCommandReply             = model.TriggerModeCommandReply
)

const (
	AdminModeAnybody  = model.AdminModeAnybody
	AdminModeAdmins   = model.AdminModeAdmins
	AdminModeNotAdmin = model.AdminModeNotAdmin
)

const (
	MatchTypeFull      = model.MatchTypeFull
	MatchTypePartial   = model.MatchTypePartial
	MatchTypeRegex     = model.MatchTypeRegex
	MatchTypeStarts    = model.MatchTypeStarts
	MatchTypeEnds      = model.MatchTypeEnds
	MatchTypeIdle      = model.MatchTypeIdle
	MatchTypeNewMember = model.MatchTypeNewMember
)

const (
	ActionTypeSend         = model.ActionTypeSend
	ActionTypeDelete       = model.ActionTypeDelete
	ActionTypeGPTPrompt    = model.ActionTypeGPTPrompt
	ActionTypeGPTImage     = model.ActionTypeGPTImage
	ActionTypeSearchImage  = model.ActionTypeSearchImage
	ActionTypeSpotifyMusic = model.ActionTypeSpotifyMusic
	ActionTypeMediaAudio   = model.ActionTypeMediaAudio
)
