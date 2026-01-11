import { writable } from 'svelte/store';
import type { EventData } from './types';
import { API_BASE } from './config';

export interface RealtimeEvent {
	type: 'person_updated' | 'contradiction_added';
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
			console.log('SSE connection opened');
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
					console.log(`Attempting to reconnect (${this.reconnectAttempts}/${this.maxReconnectAttempts})`);
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
	const unsubscribe = realtimeEvents.subscribe(events => {
		// Process new events
		const relevantEvents = events.filter(event => personId === -1 || event.personId === personId);
		relevantEvents.forEach(callback);

		// Keep only recent events to prevent memory leaks
		if (events.length > 100) {
			realtimeEvents.update(currentEvents => currentEvents.slice(-50));
		}
	});

	return unsubscribe;
}

// Helper function to clear old events
export function clearOldEvents() {
	realtimeEvents.set([]);
}