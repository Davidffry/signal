// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2023 Scott Weber
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package signalmeow

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	signalpb "go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
)

type GroupMemberRole int32

const (
	// Note: right now we assume these match the equivalent values in the protobuf (signalpb.Member_Role)
	GroupMember_UNKNOWN       GroupMemberRole = 0
	GroupMember_DEFAULT       GroupMemberRole = 1
	GroupMember_ADMINISTRATOR GroupMemberRole = 2
)

type GroupMember struct {
	UserID           uuid.UUID
	Role             GroupMemberRole
	ProfileKey       libsignalgo.ProfileKey
	JoinedAtRevision uint32
	//Presentation     []byte
}

type Group struct {
	groupMasterKey  types.SerializedGroupMasterKey // We should keep this relatively private
	GroupIdentifier types.GroupIdentifier          // This is what we should use to identify a group outside this file

	Title                        string
	AvatarPath                   string
	Members                      []*GroupMember
	Description                  string
	AnnouncementsOnly            bool
	Revision                     uint32
	DisappearingMessagesDuration uint32
	//PublicKey                  *libsignalgo.PublicKey
	//AccessControl              *AccessControl
	//PendingMembers             []*PendingMember
	//RequestingMembers          []*RequestingMember
	//InviteLinkPassword         []byte
	//BannedMembers              []*BannedMember
}

type GroupAuth struct {
	Username string
	Password string
}

func (cli *Client) fetchNewGroupCreds(ctx context.Context, today time.Time) (*GroupCredentials, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "fetch new group creds").
		Logger()
	sevenDaysOut := today.Add(7 * 24 * time.Hour)
	path := fmt.Sprintf("/v1/certificate/auth/group?redemptionStartSeconds=%d&redemptionEndSeconds=%d", today.Unix(), sevenDaysOut.Unix())
	authRequest := web.CreateWSRequest(http.MethodGet, path, nil, nil, nil)
	resp, err := cli.AuthedWS.SendRequest(ctx, authRequest)
	if err != nil {
		return nil, fmt.Errorf("SendRequest error: %w", err)
	}
	if *resp.Status != 200 {
		return nil, fmt.Errorf("bad status code fetching group creds: %d", *resp.Status)
	}

	var creds GroupCredentials
	err = json.Unmarshal(resp.Body, &creds)
	if err != nil {
		log.Err(err).Msg("json.Unmarshal error")
		return nil, err
	}
	// make sure pni matches device pni
	if creds.PNI != cli.Store.PNI {
		err := fmt.Errorf("creds.PNI != d.PNI")
		log.Err(err).Msg("creds.PNI != d.PNI")
		return nil, err
	}
	return &creds, nil
}

func (cli *Client) getCachedAuthorizationForToday(today time.Time) *GroupCredential {
	if cli.GroupCredentials == nil {
		// No cached credentials
		return nil
	}
	allCreds := cli.GroupCredentials
	// Get the credential for today
	for _, cred := range allCreds.Credentials {
		if cred.RedemptionTime == today.Unix() {
			return &cred
		}
	}
	return nil
}

func (cli *Client) GetAuthorizationForToday(ctx context.Context, masterKey libsignalgo.GroupMasterKey) (*GroupAuth, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "get authorization for today").
		Logger()
	// Timestamps for the start of today, and 7 days later
	today := time.Now().Truncate(24 * time.Hour)

	todayCred := cli.getCachedAuthorizationForToday(today)
	if todayCred == nil {
		creds, err := cli.fetchNewGroupCreds(ctx, today)
		if err != nil {
			return nil, fmt.Errorf("fetchNewGroupCreds error: %w", err)
		}
		cli.GroupCredentials = creds
		todayCred = cli.getCachedAuthorizationForToday(today)
	}
	if todayCred == nil {
		return nil, fmt.Errorf("couldn't get credential for today")
	}

	//TODO: cache cred after unmarshalling
	redemptionTime := uint64(todayCred.RedemptionTime)
	credential := todayCred.Credential
	authCredentialResponse, err := libsignalgo.NewAuthCredentialWithPniResponse(credential)
	if err != nil {
		log.Err(err).Msg("NewAuthCredentialWithPniResponse error")
		return nil, err
	}

	// Receive the auth credential
	authCredential, err := libsignalgo.ReceiveAuthCredentialWithPni(
		prodServerPublicParams,
		cli.Store.ACI,
		cli.Store.PNI,
		redemptionTime,
		*authCredentialResponse,
	)
	if err != nil {
		log.Err(err).Msg("ReceiveAuthCredentialWithPni error")
		return nil, err
	}

	// get auth presentation
	groupSecretParams, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(masterKey)
	if err != nil {
		log.Err(err).Msg("DeriveGroupSecretParamsFromMasterKey error")
		return nil, err
	}
	authCredentialPresentation, err := libsignalgo.CreateAuthCredentialWithPniPresentation(
		prodServerPublicParams,
		libsignalgo.GenerateRandomness(),
		groupSecretParams,
		*authCredential,
	)
	if err != nil {
		log.Err(err).Msg("CreateAuthCredentialWithPniPresentation error")
		return nil, err
	}
	groupPublicParams, err := groupSecretParams.GetPublicParams()
	if err != nil {
		log.Err(err).Msg("GetPublicParams error")
		return nil, err
	}

	return &GroupAuth{
		Username: hex.EncodeToString(groupPublicParams[:]),
		Password: hex.EncodeToString(*authCredentialPresentation),
	}, nil
}

func masterKeyToBytes(groupMasterKey types.SerializedGroupMasterKey) libsignalgo.GroupMasterKey {
	// We are very tricksy, groupMasterKey is just base64 encoded group master key :O
	masterKeyBytes, err := base64.StdEncoding.DecodeString(string(groupMasterKey))
	if err != nil {
		panic(fmt.Errorf("we should always be able to decode groupMasterKey into masterKeyBytes: %w", err))
	}
	return libsignalgo.GroupMasterKey(masterKeyBytes)
}

func masterKeyFromBytes(masterKey libsignalgo.GroupMasterKey) types.SerializedGroupMasterKey {
	return types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(masterKey[:]))
}

func groupIdentifierFromMasterKey(masterKey types.SerializedGroupMasterKey) (types.GroupIdentifier, error) {
	groupSecretParams, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(masterKeyToBytes(masterKey))
	if err != nil {
		return "", fmt.Errorf("DeriveGroupSecretParamsFromMasterKey error: %w", err)
	}
	// Get the "group identifier" that isn't just the master key
	groupPublicParams, err := groupSecretParams.GetPublicParams()
	if err != nil {
		return "", fmt.Errorf("GetPublicParams error: %w", err)
	}
	groupIdentifier, err := libsignalgo.GetGroupIdentifier(*groupPublicParams)
	if err != nil {
		return "", fmt.Errorf("GetGroupIdentifier error: %w", err)
	}
	base64GroupIdentifier := base64.StdEncoding.EncodeToString(groupIdentifier[:])
	gid := types.GroupIdentifier(base64GroupIdentifier)
	return gid, nil
}

func decryptGroup(ctx context.Context, encryptedGroup *signalpb.Group, groupMasterKey types.SerializedGroupMasterKey) (*Group, error) {
	log := zerolog.Ctx(ctx).With().Str("action", "decrypt group").Logger()
	decryptedGroup := &Group{
		groupMasterKey: groupMasterKey,
	}

	groupSecretParams, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(masterKeyToBytes(groupMasterKey))
	if err != nil {
		log.Err(err).Msg("DeriveGroupSecretParamsFromMasterKey error")
		return nil, err
	}

	gid, err := groupIdentifierFromMasterKey(groupMasterKey)
	if err != nil {
		log.Err(err).Msg("groupIdentifierFromMasterKey error")
		return nil, err
	}
	decryptedGroup.GroupIdentifier = gid

	titleBlob, err := decryptGroupPropertyIntoBlob(groupSecretParams, encryptedGroup.Title)
	if err != nil {
		return nil, err
	}
	// The actual title is in the blob
	decryptedGroup.Title = cleanupStringProperty(titleBlob.GetTitle())

	descriptionBlob, err := decryptGroupPropertyIntoBlob(groupSecretParams, encryptedGroup.Description)
	if err == nil {
		// treat a failure in obtaining the description as non-fatal
		decryptedGroup.Description = cleanupStringProperty(descriptionBlob.GetDescription())
	}

	if encryptedGroup.DisappearingMessagesTimer != nil && len(encryptedGroup.DisappearingMessagesTimer) > 0 {
		timerBlob, err := decryptGroupPropertyIntoBlob(groupSecretParams, encryptedGroup.DisappearingMessagesTimer)
		if err != nil {
			return nil, err
		}
		decryptedGroup.DisappearingMessagesDuration = timerBlob.GetDisappearingMessagesDuration()
	}

	// These aren't encrypted
	decryptedGroup.AvatarPath = encryptedGroup.Avatar
	decryptedGroup.Revision = encryptedGroup.Revision

	// Decrypt members
	decryptedGroup.Members = make([]*GroupMember, 0)
	for _, member := range encryptedGroup.Members {
		if member == nil {
			continue
		}
		encryptedUserID := libsignalgo.UUIDCiphertext(member.UserId)
		userID, err := groupSecretParams.DecryptUUID(encryptedUserID)
		if err != nil {
			log.Err(err).Msg("DecryptUUID UserId error")
			return nil, err
		}
		encryptedProfileKey := libsignalgo.ProfileKeyCiphertext(member.ProfileKey)
		profileKey, err := groupSecretParams.DecryptProfileKey(encryptedProfileKey, userID)
		if err != nil {
			log.Err(err).Msg("DecryptProfileKey ProfileKey error")
			return nil, err
		}
		decryptedGroup.Members = append(decryptedGroup.Members, &GroupMember{
			UserID:           userID,
			ProfileKey:       *profileKey,
			Role:             GroupMemberRole(member.Role),
			JoinedAtRevision: member.JoinedAtRevision,
		})
	}

	return decryptedGroup, nil
}

func decryptGroupPropertyIntoBlob(groupSecretParams libsignalgo.GroupSecretParams, encryptedProperty []byte) (*signalpb.GroupAttributeBlob, error) {
	decryptedProperty, err := groupSecretParams.DecryptBlobWithPadding(encryptedProperty)
	if err != nil {
		return nil, fmt.Errorf("error decrypting blob with padding: %w", err)
	}
	var propertyBlob signalpb.GroupAttributeBlob
	err = proto.Unmarshal(decryptedProperty, &propertyBlob)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling blob: %w", err)
	}
	return &propertyBlob, nil
}

func cleanupStringProperty(property string) string {
	// strip non-printable characters from the string
	property = strings.Map(cleanupStringMapping, property)
	// strip \n and \t from start and end of the property if it exists
	return strings.TrimSpace(property)
}

func cleanupStringMapping(r rune) rune {
	if unicode.IsGraphic(r) {
		return r
	}
	return -1
}

func decryptGroupAvatar(encryptedAvatar []byte, groupMasterKey types.SerializedGroupMasterKey) ([]byte, error) {
	groupSecretParams, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(masterKeyToBytes(groupMasterKey))
	if err != nil {
		return nil, fmt.Errorf("error deriving group secret params from master key: %w", err)
	}
	avatarBlob, err := decryptGroupPropertyIntoBlob(groupSecretParams, encryptedAvatar)
	if err != nil {
		return nil, err
	}
	// The actual avatar is in the blob
	decryptedImage := avatarBlob.GetAvatar()

	return decryptedImage, nil
}

func groupMetadataForDataMessage(group Group) *signalpb.GroupContextV2 {
	masterKey := masterKeyToBytes(group.groupMasterKey)
	masterKeyBytes := masterKey[:]
	return &signalpb.GroupContextV2{
		MasterKey: masterKeyBytes,
		Revision:  &group.Revision,
	}
}

func (cli *Client) fetchGroupByID(ctx context.Context, gid types.GroupIdentifier) (*Group, error) {
	groupMasterKey, err := cli.Store.GroupStore.MasterKeyFromGroupIdentifier(ctx, gid)
	if err != nil {
		return nil, fmt.Errorf("failed to get group master key: %w", err)
	}
	if groupMasterKey == "" {
		return nil, fmt.Errorf("No group master key found for group identifier %s", gid)
	}
	masterKeyBytes := masterKeyToBytes(groupMasterKey)
	groupAuth, err := cli.GetAuthorizationForToday(ctx, masterKeyBytes)
	if err != nil {
		return nil, err
	}
	opts := &web.HTTPReqOpt{
		Username:    &groupAuth.Username,
		Password:    &groupAuth.Password,
		ContentType: web.ContentTypeProtobuf,
		Host:        web.StorageHostname,
	}
	response, err := web.SendHTTPRequest(ctx, http.MethodGet, "/v1/groups", opts)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("fetchGroupByID SendHTTPRequest bad status: %d", response.StatusCode)
	}
	var encryptedGroup signalpb.Group
	groupBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	err = proto.Unmarshal(groupBytes, &encryptedGroup)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal group: %w", err)
	}

	group, err := decryptGroup(ctx, &encryptedGroup, groupMasterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt group: %w", err)
	}

	// Store the profile keys in case they're new
	for _, member := range group.Members {
		err = cli.Store.ProfileKeyStore.StoreProfileKey(ctx, member.UserID, member.ProfileKey)
		if err != nil {
			return nil, fmt.Errorf("failed to store profile key: %w", err)
		}
	}
	return group, nil
}

func (cli *Client) DownloadGroupAvatar(ctx context.Context, group *Group) ([]byte, error) {
	username, password := cli.Store.BasicAuthCreds()
	opts := &web.HTTPReqOpt{
		Host:     web.CDN1Hostname,
		Username: &username,
		Password: &password,
	}
	resp, err := web.SendHTTPRequest(ctx, http.MethodGet, group.AvatarPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected response status %d", resp.StatusCode)
	}
	encryptedAvatar, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	decrypted, err := decryptGroupAvatar(encryptedAvatar, group.groupMasterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt avatar: %w", err)
	}
	return decrypted, nil
}

func (cli *Client) RetrieveGroupByID(ctx context.Context, gid types.GroupIdentifier, revision uint32) (*Group, error) {
	cli.initGroupCache()

	lastFetched, ok := cli.GroupCache.lastFetched[gid]
	if ok && time.Since(lastFetched) < 1*time.Hour {
		group, ok := cli.GroupCache.groups[gid]
		if ok && group.Revision >= revision {
			return group, nil
		}
	}
	group, err := cli.fetchGroupByID(ctx, gid)
	if err != nil {
		return nil, err
	}
	cli.GroupCache.groups[gid] = group
	cli.GroupCache.lastFetched[gid] = time.Now()
	return group, nil
}

// We should store the group master key in the group store as soon as we see it,
// then use the group identifier to refer to groups. As a convenience, we return
// the group identifier, which is derived from the group master key.
func (cli *Client) StoreMasterKey(ctx context.Context, groupMasterKey types.SerializedGroupMasterKey) (types.GroupIdentifier, error) {
	groupIdentifier, err := groupIdentifierFromMasterKey(groupMasterKey)
	if err != nil {
		return "", fmt.Errorf("groupIdentifierFromMasterKey error: %w", err)
	}
	err = cli.Store.GroupStore.StoreMasterKey(ctx, groupIdentifier, groupMasterKey)
	if err != nil {
		return "", fmt.Errorf("StoreMasterKey error: %w", err)
	}
	return groupIdentifier, nil
}

// We need to track active calls so we don't send too many IncomingSignalMessageCalls
// Of course for group calls Signal doesn't tell us *anything* so we're mostly just inferring
// So we just jam a new call ID in, and return true if we *think* this is a new incoming call
func (cli *Client) UpdateActiveCalls(gid types.GroupIdentifier, callID string) (isActive bool) {
	cli.initGroupCache()
	// Check to see if we currently have an active call for this group
	currentCallID, ok := cli.GroupCache.activeCalls[gid]
	if ok {
		// If we do, then this must be ending the call
		if currentCallID == callID {
			delete(cli.GroupCache.activeCalls, gid)
			return false
		}
	}
	cli.GroupCache.activeCalls[gid] = callID
	return true
}

func (cli *Client) initGroupCache() {
	if cli.GroupCache == nil {
		cli.GroupCache = &GroupCache{
			groups:      make(map[types.GroupIdentifier]*Group),
			lastFetched: make(map[types.GroupIdentifier]time.Time),
			activeCalls: make(map[types.GroupIdentifier]string),
		}
	}
}

type GroupCache struct {
	groups      map[types.GroupIdentifier]*Group
	lastFetched map[types.GroupIdentifier]time.Time
	activeCalls map[types.GroupIdentifier]string
}
