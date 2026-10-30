import type { TaskResult } from "../api/client";

export default function ConversationView({ task }: { task?: TaskResult }) {
  const messages = (task?.transcript ?? []).filter(
    (event) => event.type === "user.message" || event.type === "assistant.message",
  );

  if (!messages.length) {
    return (
      <p className="lab-note">
        No recorded conversation messages for this task. Prompts, grader feedback,
        and tool events are not reconstructed into a conversation.
      </p>
    );
  }

  return (
    <section aria-label="Task conversation">
      <p className="lab-note">
        {messages.length} recorded {messages.length === 1 ? "message" : "messages"}.
        Showing the first repetition in recorded order. Tool activity, reasoning,
        and streaming deltas are omitted; transcript completeness is not assessed.
      </p>
      {task?.responder && (
        <p className="lab-note">
          Responder recorded {task.responder.followupsSent} simulated-user{" "}
          {task.responder.followupsSent === 1 ? "follow-up" : "follow-ups"}.
          Individual user messages do not record human or simulated provenance.
        </p>
      )}
      <ol className="lab-conversation" aria-label="Recorded conversation">
        {messages.map((event, index) => (
          <li
            key={index}
            className={event.type === "user.message" ? "lab-message-user" : "lab-message-agent"}
          >
            <div className="lab-message-heading">
              <strong>
                {event.type === "user.message" ? "User" : "Agent under test"}
              </strong>
              <span>Message {index + 1}</span>
            </div>
            {typeof event.content !== "string" || event.content === "" ? (
              <p className="lab-note">No message text recorded.</p>
            ) : (
              <div className="lab-message-content">{event.content}</div>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}
