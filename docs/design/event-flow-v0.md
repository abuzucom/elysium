# Private event, admission, and session flow, version 0

Status: Approved v0 by the active human on 2026-11-14 with the resolved
decisions below. Any change to these invitation and authorization
contracts requires new approval (bootstrap section 5).

## Purpose

Gate 5 needs the smallest private event path that lets an invited
listener hear the live stream end to end. This document proposes the
entities, flows, tokens, and offline behavior for that path. Long-term
cloud planning and full event UX stay out of scope.

## Constraints from the approved baseline

- Events are invite-only and non-social. No public discovery or ticket
  sales.
- Admission comes from a verified invited email or a local staff
  approval. A device-bound on-site request may show a six-digit
  confirmation identifier for staff matching. That number is not a
  secret.
- Sessions are event-bound and device-bound. Listener tokens are signed
  and short-lived, with proof of possession where required.
- Network admission (for example a Wi-Fi QR code) never grants stream
  access by itself.
- Venue-local listening and basic event operations work without WAN.
- Phone wall time never decides token expiry or entry eligibility.
- Logs, fixtures, and artifacts hold no credentials or personal data.

## Components

| Component | Location | Role |
| --- | --- | --- |
| Event service | Venue controller, VLAN 30 | Events, invitations, admissions, devices, listener tokens |
| VenueTimeService | Venue controller, VLAN 30 | Authoritative venue UTC for every expiry decision |
| LiveKit | Venue controller, VLAN 30 | Media fanout. Accepts only tokens the event service signs |
| Staff console | Staff phone or laptop | Admission approval and event operations |
| Listener app | Attendee phone, VLAN 10 | Enrollment, admission request, listening |
| Cloud planning | Optional, WAN | Event planning and invitation delivery. Syncs when WAN returns |

## Entities

| Entity | Key fields | Notes |
| --- | --- | --- |
| Event | id, name, IANA zone, start and end as UTC instants, list state (Upcoming, Past, Hidden) | The organizer controls list state. Recurrence stays out of v0 |
| Invitation | id, event id, email digest, status | Holds a salted SHA-256 digest of the normalized email for matching. Delivery details stay in the planning system |
| Device | id, public key, platform | The phone generates the key pair in hardware-backed storage. The private key never leaves the device |
| Admission | id, event id, device id, method (invitation or staff approval), approver id, approved at (venue UTC), valid until (venue UTC) | Binds one device to one event |
| Staff account | id, roles | Roles: organizer, venue administrator, event manager, admissions staff, technical operator |

## Flows

### 1. Invitation enrollment (before or at the event)

1. The listener app generates a device key pair.
2. The listener enters the invited email. The event service sends a
   one-time code to that address while WAN is available.
3. The app submits the code and the device public key, signed with the
   device key.
4. The event service verifies the code, matches the invitation, and
   creates an Admission with method "invitation".

### 2. On-site staff approval (works without WAN)

1. The listener app generates a device key pair if none exists.
2. The app sends an admission request with the device public key, signed
   with the device key. The event service returns a six-digit
   confirmation identifier unique among open requests for the event.
3. The listener shows the identifier to admissions staff.
4. Staff find the request by identifier in the staff console, check the
   listener in person, and approve it.
5. The event service creates an Admission with method "staff approval"
   and records the approver.

The identifier only matches a request to a person. Approval comes from
an authenticated staff account. A guessed identifier gains nothing
without staff approval.

### 3. Listening session

1. The app requests a challenge from the event service.
2. The app signs the challenge with the device key.
3. The event service verifies the signature, finds a valid Admission for
   the event, and reads venue UTC from VenueTimeService.
4. The event service returns a LiveKit token: room equals the event,
   identity equals the device id, subscribe only, no publish, no data
   publish, short expiry.
5. The app joins LiveKit and refreshes the token by repeating steps 1 to
   4 before expiry.

## Token rules

- The event service signs LiveKit tokens with a key that stays on the
  venue controller.
- Expiry comes from venue UTC. The phone clock plays no part.
- A token grants one event room and subscription only.
- Revoking an Admission stops refresh. The existing token expires on its
  own. Removing the participant from the room ends access at once.

## Offline behavior

- The venue controller holds events, invitations, admissions, and the
  token signing key. Listening, staff approval, and refresh work without
  WAN.
- Email enrollment needs WAN for code delivery. Already enrolled devices
  keep working offline.
- Changes made offline sync to cloud planning when WAN returns. The
  venue copy wins for admissions made at the venue.

## Abuse limits

- Each source address may send at most 10 admission requests per minute.
- Open requests expire after 60 minutes.
- A device holds at most one open request per event.
- The listener network (VLAN 10) reaches only the event API, time
  services, and encrypted LiveKit services.

## Resolved decisions (2026-11-14)

| Topic | Decision |
| --- | --- |
| Listener token | 60-minute lifetime. The app refreshes after 30 minutes. Removing the participant from the room ends access at once |
| Admission validity | Scheduled event end plus a grace period. Grace defaults to 4 hours with a 6-hour maximum. The organizer or event manager sets it per event |
| Venue time unsynchronized | During holdover, tokens keep issuing with lifetimes shortened by the current uncertainty. Once unsynchronized, new tokens are refused and the technical operator receives an alert |
| Email verification | One-time code by email, entered in the app |
| Devices per invitation | One device per invitation. The device replacement procedure stays open |
| Staff sign-in | Passkeys where supported, with an Argon2id password fallback. All accounts stay local to the venue controller |
| Retention | The organizer sets retention per event for invitation digests and admission records. Default 7 days after the event, maximum 30 days |
| Admission requests | Open requests expire after 60 minutes. At most 10 requests per minute per source address |
