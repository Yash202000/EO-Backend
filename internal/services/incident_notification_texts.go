package services

import (
	"fmt"
)

// Arabic text builders for incident in-app notifications.
// English text is built inline at each call site; these produce the Arabic counterpart
// stored in subject_ar / body_ar and returned when Accept-Language: ar.

func IncidentUpdatedTextsAr(incidentNumber, title string) (subject, body string) {
	return fmt.Sprintf("تم تحديث البلاغ رقم %s", incidentNumber),
		fmt.Sprintf("تم تحديث بلاغ \"%s\" (%s)", title, incidentNumber)
}

func IncidentAssignedTransitionTextsAr(incidentNumber, title, stateName string) (subject, body string) {
	return fmt.Sprintf("بلاغ رقم %s مسند اليك", incidentNumber),
		fmt.Sprintf("بلاغ \"%s\" اسند لك. تم تغيير الحالة الى : %s", title, stateName)
}

func IncidentAssignedDirectTextsAr(incidentNumber, title string) (subject, body string) {
	return fmt.Sprintf("بلاغ رقم %s مسند اليك", incidentNumber),
		fmt.Sprintf("بلاغ \"%s\" اسند لك", title)
}

func PartialCloseExpiryTextsAr(incidentNumber, title, revertStateName, timeStr, expiresAt string) (subject, body string) {
	subject = fmt.Sprintf("البلاغ %s: موعد انتهاء الإغلاق الجزئي قريب", incidentNumber)
	body = fmt.Sprintf(
		"سيتم إعادة هذا البلاغ تلقائياً إلى '%s' إذا لم يتم إغلاقه خلال %s.\n\nرقم البلاغ: %s\nالعنوان: %s\nتاريخ الانتهاء: %s",
		revertStateName, timeStr, incidentNumber, title, expiresAt,
	)
	return
}

// SimilarIncidentTexts builds the EN and AR in-app notification shown to the creator of a
// new incident when an earlier incident exists with the same location and classification.
func SimilarIncidentTexts(prevNumber, status, statusAr string) (subject, body, subjectAr, bodyAr string) {
	subject = "Similar incident previously detected"
	body = fmt.Sprintf("This incident was also identified during the previous scan. Previous Incident: %s - %s.", prevNumber, status)
	if statusAr == "" {
		statusAr = status
	}
	subjectAr = "تم اكتشاف بلاغ مماثل سابقاً"
	bodyAr = fmt.Sprintf("تم تحديد هذا البلاغ أيضاً خلال الفحص السابق. البلاغ السابق: %s - %s.", prevNumber, statusAr)
	return
}
