package ptlib

import "PortTool/src/ptitems"

// ChannelWalk says the item runs channel by channel in a full test: a person
// applying 24 V input by input (DI level) or measuring each output under load
// (DO load). Each channel is a numbered step of its own (decision 128).
func ChannelWalk(id string, peer ptitems.Peer) bool {
	return peer == ptitems.PeerHuman && (dinWalks[id] || dropWalk[id])
}

// WalkChannels is the channels such an item walks with these parameters.
func WalkChannels(params map[string]string) []int { return dropChannels(params) }
