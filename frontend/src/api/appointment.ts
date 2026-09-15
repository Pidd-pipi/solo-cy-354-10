import request from '../utils/request'
import type { MeetupAppointment } from '../types'

type ApiBody<T> = { code: number; message: string; data: T }

export function createAppointment(orderId: number, payload: { meet_at: string; location: string }) {
  return request.post<never, ApiBody<MeetupAppointment>>(`/trade-orders/${orderId}/appointments`, payload)
}

export function listAppointments(orderId: number) {
  return request.get<never, ApiBody<MeetupAppointment[]>>(`/trade-orders/${orderId}/appointments`)
}

export function acceptAppointment(id: number) {
  return request.post<never, ApiBody<MeetupAppointment>>(`/appointments/${id}/accept`)
}

export function rejectAppointment(id: number) {
  return request.post<never, ApiBody<MeetupAppointment>>(`/appointments/${id}/reject`)
}

export function rescheduleAppointment(id: number, payload: { meet_at: string; location: string }) {
  return request.post<never, ApiBody<MeetupAppointment>>(`/appointments/${id}/reschedule`, payload)
}

export function confirmHandover(id: number) {
  return request.post<never, ApiBody<MeetupAppointment>>(`/appointments/${id}/handover-confirm`)
}
