import { writable } from 'svelte/store';
import type { EventData } from './types';
import { API_BASE } from './config';

export interface RealtimeEvent {
	type: 'person_updated' | 'contradiction_added' | 'poll_updated';
	// The person of a person event; the poll event of a poll_updated.
	personId: number;
	data: any;
}

// Store for realtime events
export const realtimeEvents = writable<RealtimeEvent[]>([]);

class RealtimeManager {
	private eventSource: EventSource | null = null;
	private lastEventId: number = 0;
	private reconnectAttempts = 0;
	private maxReconnectAttempts = 5;
	private reconnectDelay = 1000; // Start with 1 second

	connect() {
		if (this.eventSource) {
			this.eventSource.close();
		}

		const url = this.lastEventId > 0
			? `${API_BASE}/api/events?since_id=${this.lastEventId}`
			: `${API_BASE}/api/events`;

		this.eventSource = new EventSource(url);

		this.eventSource.onopen = () => {
			this.reconnectAttempts = 0;
			this.reconnectDelay = 1000;
		};

		this.eventSource.onmessage = (event) => {
			try {
				const eventData: EventData = JSON.parse(event.data);

				// Update last seen event ID for resume functionality
				this.lastEventId = Math.max(this.lastEventId, eventData.id);

				// Convert to our internal event format
				let realtimeEvent: RealtimeEvent | null = null;

				if (eventData.topic === 'person_updated' && eventData.entity_type === 'person') {
					realtimeEvent = {
						type: 'person_updated',
						personId: eventData.entity_id,
						data: eventData.payload
					};
				} else if (eventData.topic === 'contradiction_added' && eventData.entity_type === 'person') {
					realtimeEvent = {
						type: 'contradiction_added',
						personId: eventData.entity_id,
						data: eventData.payload
					};
				} else if (eventData.topic === 'poll_updated' && eventData.entity_type === 'poll_event') {
					realtimeEvent = {
						type: 'poll_updated',
						personId: eventData.entity_id,
						data: eventData.payload
					};
				}

				if (realtimeEvent) {
					realtimeEvents.update(events => [...events, realtimeEvent!]);
				}
			} catch (error) {
				console.error('Error parsing SSE event:', error);
			}
		};

		this.eventSource.onerror = (error) => {
			console.error('SSE connection error:', error);

			if (this.eventSource) {
				this.eventSource.close();
				this.eventSource = null;
			}

			// Attempt to reconnect with exponential backoff
			if (this.reconnectAttempts < this.maxReconnectAttempts) {
				this.reconnectAttempts++;
				setTimeout(() => {
					this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000); // Max 30 seconds
					this.connect();
				}, this.reconnectDelay);
			} else {
				console.error('Max reconnection attempts reached');
			}
		};
	}

	disconnect() {
		if (this.eventSource) {
			this.eventSource.close();
			this.eventSource = null;
		}
	}

	// Get the last event ID for persistence across page reloads
	getLastEventId(): number {
		return this.lastEventId;
	}

	// Set the last event ID (useful for resuming from localStorage)
	setLastEventId(id: number) {
		this.lastEventId = id;
	}
}

// Singleton instance
export const realtimeManager = new RealtimeManager();

// Helper function to subscribe to events for a specific person
// Pass -1 as personId to listen to all person events
export function subscribeToPersonEvents(personId: number, callback: (event: RealtimeEvent) => void) {
	let lastProcessedIndex = -1;
	let initialized = false;

	const unsubscribe = realtimeEvents.subscribe(events => {
		// On first subscription, mark all existing events as "seen" without processing
		// This prevents processing historical events that were already in the store
		if (!initialized) {
			lastProcessedIndex = events.length - 1;
			initialized = true;
			return;
		}

		// Only process NEW events (after lastProcessedIndex)
		for (let i = lastProcessedIndex + 1; i < events.length; i++) {
			const event = events[i];
			if (event.type === 'poll_updated') continue;
			if (personId === -1 || event.personId === personId) {
				callback(event);
			}
		}
		lastProcessedIndex = events.length - 1;

		// Keep only recent events to prevent memory leaks
		if (events.length > 100) {
			realtimeEvents.update(currentEvents => currentEvents.slice(-50));
			lastProcessedIndex = Math.max(-1, lastProcessedIndex - 50);
		}
	});

	return unsubscribe;
}

// subscribeToPollEvents calls back when a ballot of the poll event changes.
export function subscribeToPollEvents(eventId: number, callback: () => void) {
	// The last event handled, by identity: the store gets trimmed, so indexes move.
	let last: RealtimeEvent | undefined;
	let initialized = false;
	return realtimeEvents.subscribe((events) => {
		const start = initialized ? (last ? events.lastIndexOf(last) + 1 : 0) : events.length;
		initialized = true;
		if (start < events.length && events.slice(start).some((e) => e.type === 'poll_updated' && e.personId === eventId)) {
			callback();
		}
		last = events[events.length - 1];
	});
}

// Helper function to clear old events
export function clearOldEvents() {
	realtimeEvents.set([]);
}