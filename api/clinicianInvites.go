package api

import (
	"context"
	"encoding/json"
	"fmt"
	oapiTypes "github.com/deepmap/oapi-codegen/pkg/types"
	clinics "github.com/tidepool-org/clinic/client"
	"github.com/tidepool-org/go-common/clients/shoreline"
	"github.com/tidepool-org/go-common/clients/status"
	"github.com/tidepool-org/hydrophone/models"
	"log"
	"net/http"
)

type ClinicianInvite struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

//Send an invite to become a clinic member
func (a *Api) SendClinicianInvite(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		clinicId := vars["clinicId"]

		if err := a.assertClinicAdmin(ctx, clinicId, token, res); err != nil {
			return
		}

		clinic, err := a.clinics.GetClinicWithResponse(ctx, clinicId)
		if err != nil || clinic == nil || clinic.JSON200 == nil {
			a.sendError(res, http.StatusInternalServerError, STATUS_ERR_FINDING_CLINIC, err)
			return
		}

		defer req.Body.Close()
		var body = &ClinicianInvite{}
		if err := json.NewDecoder(req.Body).Decode(body); err != nil {
			log.Printf("SendClinicianInvite: error decoding invite to %v\n", err)
			statusErr := &status.StatusError{Status: status.NewStatus(http.StatusBadRequest, STATUS_ERR_DECODING_CONFIRMATION)}
			a.sendModelAsResWithStatus(res, statusErr, http.StatusBadRequest)
			return
		}

		confirmation, _ := models.NewConfirmation(models.TypeClinicianInvite, models.TemplateNameClinicianInvite, token.UserID)
		confirmation.Email = body.Email
		confirmation.ClinicId = string(clinic.JSON200.Id)
		confirmation.Creator.ClinicId = string(clinic.JSON200.Id)
		confirmation.Creator.ClinicName = clinic.JSON200.Name

		invitedUsr := a.findExistingUser(body.Email, a.sl.TokenProvide())
		if invitedUsr != nil && invitedUsr.UserID != "" {
			confirmation.UserId = invitedUsr.UserID
		}

		response, err := a.clinics.CreateClinicianWithResponse(ctx, clinicId, clinics.CreateClinicianJSONRequestBody{
			InviteId: &confirmation.Key,
			Email:    oapiTypes.Email(body.Email),
			Roles:    body.Roles,
		})
		if err != nil {
			a.sendError(res, http.StatusInternalServerError, STATUS_ERR_FINDING_CLINIC, err)
			return
		}
		if response.StatusCode() != http.StatusOK {
			res.Header().Set("content-type", "application/json")
			res.WriteHeader(response.StatusCode())
			res.Write(response.Body)
			return
		}

		a.sendClinicianConfirmation(res, req, confirmation)
		return
	}
}

//Send an invite to become a clinic member
func (a *Api) ResendClinicianInvite(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		clinicId := vars["clinicId"]
		inviteId := vars["inviteId"]

		if err := a.assertClinicAdmin(ctx, clinicId, token, res); err != nil {
			return
		}

		clinic, err := a.clinics.GetClinicWithResponse(ctx, clinicId)
		if err != nil || clinic == nil || clinic.JSON200 == nil {
			a.sendError(res, http.StatusInternalServerError, STATUS_ERR_FINDING_CLINIC, err)
			return
		}

		inviteReponse, err := a.clinics.GetInvitedClinicianWithResponse(ctx, clinicId, inviteId)
		if err != nil {
			a.sendError(res, http.StatusInternalServerError, STATUS_ERR_FINDING_CLINIC, err)
			return
		}
		if inviteReponse.StatusCode() != http.StatusOK || inviteReponse.JSON200 == nil {
			res.Header().Set("content-type", "application/json")
			res.WriteHeader(inviteReponse.StatusCode())
			res.Write(inviteReponse.Body)
			return
		}

		filter := &models.Confirmation{
			Key:    inviteId,
			Type:   models.TypeClinicianInvite,
			Status: models.StatusPending,
		}
		confirmation, err := a.findExistingConfirmation(req.Context(), filter, res)
		if err != nil {
			log.Printf("ResendClinicianInvite error while finding confirmation [%s]\n", err.Error())
			a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
			return
		}
		if confirmation == nil {
			confirmation, _ := models.NewConfirmation(models.TypeClinicianInvite, models.TemplateNameClinicianInvite, token.UserID)
			confirmation.Key = inviteId
		}

		confirmation.Email = string(inviteReponse.JSON200.Email)
		confirmation.ClinicId = string(clinic.JSON200.Id)
		confirmation.Creator.ClinicId = string(clinic.JSON200.Id)
		confirmation.Creator.ClinicName = clinic.JSON200.Name

		invitedUsr := a.findExistingUser(confirmation.Email, a.sl.TokenProvide())
		if invitedUsr != nil && invitedUsr.UserID != "" {
			confirmation.UserId = invitedUsr.UserID
		}

		a.sendClinicianConfirmation(res, req, confirmation)
		return
	}
}

// Get the still-pending invitations for a clinician
func (a *Api) GetClinicianInvitations(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		userId := vars["userId"]

		invitedUsr := a.findExistingUser(userId, req.Header.Get(TP_SESSION_TOKEN))

		// Tokens only legit when for same userid
		if userId != token.UserID || invitedUsr == nil {
			log.Printf("GetClinicianInvitations %s ", STATUS_UNAUTHORIZED)
			a.sendModelAsResWithStatus(res, status.StatusError{Status: status.NewStatus(http.StatusUnauthorized, STATUS_UNAUTHORIZED)}, http.StatusUnauthorized)
			return
		}

		found, err := a.Store.FindConfirmations(ctx, &models.Confirmation{Email: invitedUsr.Emails[0], Type: models.TypeClinicianInvite}, models.StatusPending)
		if invites := a.checkFoundConfirmations(res, found, err); invites != nil {
			a.ensureIdSet(req.Context(), userId, invites)
			log.Printf("GetClinicianInvitations: found and have checked [%d] invites ", len(invites))
			a.logMetric("get_clinician_invitations", req)
			a.sendModelAsResWithStatus(res, invites, http.StatusOK)
			return
		}
	}
}

// Accept the given invite
func (a *Api) AcceptClinicianInvite(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		userId := vars["userId"]
		inviteId := vars["inviteId"]

		accept := &models.Confirmation{
			Key:    inviteId,
			UserId: userId,
			Type:   models.TypeClinicianInvite,
			Status: models.StatusPending,
		}
		conf, err := a.findExistingConfirmation(req.Context(), accept, res)
		if err != nil {
			log.Printf("AcceptClinicianInvite error while finding confirmation [%s]\n", err.Error())
			a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
			return
		}
		if err := a.assertRecipientAuthorized(res, req, token, conf); err != nil {
			log.Println(err)
			return
		}

		association := clinics.AssociateClinicianToUserJSONRequestBody{UserId: token.UserID}
		response, err := a.clinics.AssociateClinicianToUserWithResponse(ctx, conf.ClinicId, inviteId, association)
		if err != nil || response.StatusCode() != http.StatusOK {
			a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
			return
		}

		conf.UpdateStatus(models.StatusCompleted)
		if !a.addOrUpdateConfirmation(req.Context(), conf, res) {
			statusErr := &status.StatusError{Status: status.NewStatus(http.StatusInternalServerError, STATUS_ERR_SAVING_CONFIRMATION)}
			log.Println("AcceptClinicianInvite ", statusErr.Error())
			a.sendModelAsResWithStatus(res, statusErr, http.StatusInternalServerError)
			return
		}

		a.logMetric("accept_clinician_invite", req)
		res.WriteHeader(http.StatusOK)
		res.Write(response.Body)
		return
	}
}

// Dismiss invite
func (a *Api) DismissClinicianInvite(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		userId := vars["userId"]
		inviteId := vars["inviteId"]

		filter := &models.Confirmation{
			Key:    inviteId,
			UserId: userId,
			Type:   models.TypeClinicianInvite,
			Status: models.StatusPending,
		}
		conf, err := a.findExistingConfirmation(ctx, filter, res)
		if err != nil {
			log.Printf("DismissClinicianInvite error while finding confirmation [%s]\n", err.Error())
			a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
			return
		}
		if err := a.assertRecipientAuthorized(res, req, token, conf); err != nil {
			log.Println(err)
			return
		}

		a.cancelClinicianInviteWithStatus(res, req, conf, models.StatusDeclined)
	}
}

// Cancel invite
func (a *Api) CancelClinicianInvite(res http.ResponseWriter, req *http.Request, vars map[string]string) {
	if token := a.token(res, req); token != nil {
		ctx := req.Context()
		clinicId := vars["clinicId"]
		inviteId := vars["inviteId"]

		if err := a.assertClinicAdmin(ctx, clinicId, token, res); err != nil {
			log.Println(err.Error())
			return
		}

		filter := &models.Confirmation{
			Key:      inviteId,
			ClinicId: clinicId,
			Type:     models.TypeClinicianInvite,
			Status:   models.StatusPending,
		}
		conf, err := a.findExistingConfirmation(ctx, filter, res)
		if err != nil {
			log.Printf("cancelClinicianInvite error while finding confirmation [%s]\n", err.Error())
			a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
			return
		}
		if conf == nil {
			statusErr := &status.StatusError{Status: status.NewStatus(http.StatusNotFound, statusInviteNotFoundMessage)}
			log.Println("cancelClinicianInvite ", statusErr.Error())
			a.sendModelAsResWithStatus(res, statusErr, http.StatusNotFound)
			return
		}
		a.cancelClinicianInviteWithStatus(res, req, conf, models.StatusCanceled)
	}
}

func (a *Api) sendClinicianConfirmation(res http.ResponseWriter, req *http.Request, confirmation *models.Confirmation) {
	ctx := req.Context()
	if a.addOrUpdateConfirmation(ctx, confirmation, res) {
		a.logMetric("clinician_invite_created", req)

		if err := a.addProfile(confirmation); err != nil {
			log.Println("SendClinicianInvite: ", err.Error())
		} else {
			fullName := confirmation.Creator.Profile.FullName

			var webPath = "signup"
			if confirmation.UserId != "" {
				webPath = "login"
			}

			emailContent := map[string]interface{}{
				"ClinicName":  confirmation.Creator.ClinicName,
				"CreatorName": fullName,
				"Email":       confirmation.Email,
				"WebPath":     webPath,
			}

			if a.createAndSendNotification(req, confirmation, emailContent) {
				a.logMetric("clinician_invite_sent", req)
			}
		}

		a.sendModelAsResWithStatus(res, confirmation, http.StatusOK)
		return
	}
}

func (a *Api) assertRecipientAuthorized(res http.ResponseWriter, req *http.Request, token *shoreline.TokenData, confirmation *models.Confirmation) (err error) {
	// Do not allow servers to handle actions on behalf of users
	if token.IsServer {
		err = &status.StatusError{Status: status.NewStatus(http.StatusUnauthorized, STATUS_UNAUTHORIZED)}
		a.sendModelAsResWithStatus(res, err, http.StatusUnauthorized)
		return err
	}

	invitedUsr := a.findExistingUser(token.UserID, req.Header.Get(TP_SESSION_TOKEN))
	if invitedUsr == nil || confirmation == nil || confirmation.Email != invitedUsr.Emails[0] {
		log.Println("DismissClinicianInvite ", STATUS_UNAUTHORIZED)
		err := &status.StatusError{Status: status.NewStatus(http.StatusUnauthorized, STATUS_UNAUTHORIZED)}
		a.sendModelAsResWithStatus(res, err, http.StatusUnauthorized)
		return err
	}

	return nil
}

func (a *Api) cancelClinicianInviteWithStatus(res http.ResponseWriter, req *http.Request, conf *models.Confirmation, statusUpdate models.Status) {
	ctx := req.Context()
	response, err := a.clinics.DeleteInvitedClinicianWithResponse(ctx, conf.ClinicId, conf.Key)
	if err != nil || (response.StatusCode() != http.StatusOK && response.StatusCode() != http.StatusNotFound) {
		log.Printf("cancelClinicianInvite error while finding confirmation [%s]\n", err)
		a.sendModelAsResWithStatus(res, err, http.StatusInternalServerError)
		return
	}

	conf.UpdateStatus(statusUpdate)
	if !a.addOrUpdateConfirmation(ctx, conf, res) {
		statusErr := &status.StatusError{Status: status.NewStatus(http.StatusInternalServerError, STATUS_ERR_SAVING_CONFIRMATION)}
		log.Println("cancelClinicianInvite ", statusErr.Error())
		a.sendModelAsResWithStatus(res, statusErr, http.StatusInternalServerError)
		return
	}

	a.logMetric("dismiss_clinician_invite", req)
	res.WriteHeader(http.StatusOK)
	res.Write([]byte(STATUS_OK))
	return
}


func (a *Api) assertClinicAdmin(ctx context.Context, clinicId string, token *shoreline.TokenData, res http.ResponseWriter) error {
	// Non-server tokens only legit when for same userid
	if !token.IsServer {
		if result, err := a.clinics.GetClinicianWithResponse(ctx, clinicId, token.UserID); err != nil || result.StatusCode() == http.StatusInternalServerError {
			a.sendError(res, http.StatusInternalServerError, STATUS_ERR_FINDING_USR, err)
			return err
		} else if result.StatusCode() != http.StatusOK {
			a.sendModelAsResWithStatus(res, status.StatusError{Status: status.NewStatus(http.StatusUnauthorized, STATUS_UNAUTHORIZED)}, http.StatusUnauthorized)
			return fmt.Errorf("unexpected status code %v when fetching clinician %v from clinic %v", result.StatusCode(), clinicId, token.UserID)
		} else {
			clinician := result.JSON200
			for _, role := range clinician.Roles {
				if role == "CLINIC_ADMIN" {
					return nil
				}
			}
			a.sendModelAsResWithStatus(res, status.StatusError{Status: status.NewStatus(http.StatusUnauthorized, STATUS_UNAUTHORIZED)}, http.StatusUnauthorized)
			return fmt.Errorf("the clinician doesn't have the required permissions %v", clinician.Roles)
		}
	}
	return nil
}
