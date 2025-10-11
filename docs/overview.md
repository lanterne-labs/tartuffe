To structure the document more effectively and remove ambiguities, I'll break it down into clear sections, correct errors, and add any necessary details. Here's a revised version:

---

# Digital Mirror: Product Scope and Requirements

## 1. Product Overview

**Digital Mirror** is a service that computes an "honesty factor" for individuals based on publicly available information found online. The honesty factor is derived from analyzing "facts" gathered from a person's digital footprint.

### 1.1 Key Concepts

- **Honesty Factor**: A score that reflects the consistency and integrity of a person's public statements and actions.
- **Facts**: Publicly available information about a person, such as statements, actions, or opinions.
- **Investigative Journalism on Demand**: The product aims to provide a comprehensive analysis of a person's public information, similar to investigative journalism, but using automated tools.

### 1.2 Long-Term Vision

The ultimate goal is to enable anyone to compute the honesty factor for any individual or entity (e.g., companies, governments) using publicly available information.

### 1.3 Short-Term Goal

To minimize complexity and cost, the initial version will focus on a permissioned set of public figures, particularly in the political realm.

## 2. Product Features

### 2.1 Figures Listing Page

- **Description**: Displays a list of public figures with their computed honesty factor.
- **Functionality**: Users can see the honesty factor next to each public figure's name.

### 2.2 Facts Listing Page

- **Description**: Displays a list of facts for a selected public figure.
- **Functionality**:
  - Each fact includes a date and a link to the original source.
  - Inconsistent facts are grouped together with an explanation of the contradiction.
  - Raw information is not stored; only links to the original sources are provided.

## 3. Architecture

### 3.1 Website

- **Frontend and Backend**: Standardized architecture with a frontend and backend.
- **Database**: Stores all information displayed on the website. The database format will be defined based on product requirements.
- **Data Flow**: The database is primarily written by the Information-Gathering System, and the website reads from it.

### 3.2 Information-Gathering System

- **Description**: A background system responsible for gathering and analyzing facts using advanced AI and LLMs.
- **Components**:
  - **SEARCHER Component**:
    - **Function**: Searches for new facts about entities.
    - **Features**:
      - Accesses previous facts stored for an entity.
      - Regularly updates the fact database.
      - Uses search engines and hints to find information.
      - Supports plugins to search different parts of the web (e.g., general web pages, social networks, IPFS).
      - Understands various information formats (text, audio, video).
      - Stores each fact with a date, link, and a succinct summary.
  - **ANALYZER Component**:
    - **Function**: Compares new facts with previously stored facts to identify inconsistencies.
    - **Features**:
      - Adjusts the honesty factor based on inconsistencies.
      - Stores explanations for conflicting facts.

## 4. Contextual Challenges

- **Context Understanding**: Facts must be understood within their context. Incomplete facts should be further analyzed or ignored.
- **Examples**:
  - Clear fact: "Person X voted for the law reducing illegal immigration."
  - Incomplete fact: "Person X disapproves of person Y’s decision." (Requires additional context).

## 5. Technological Requirements

### 5.1 Cost

- **Budget**: The initial MVP should cost less than $100/month.
- **Free Tiers**: Utilize services with free tiers when possible.

### 5.2 DevOps

- **Automation**: Minimize manual DevOps work.
- **Examples**: Use Vercel for the frontend and Supabase for the database and AI integration.

### 5.3 AI Integration

- **Agnostic System**: The Information-Gathering System should be agnostic to the AI system used.
- **Endpoints**: Communicate via OpenAI-compatible endpoints (e.g., OpenAI, Mistral, DeepSeek, local OpenAI-compatible servers).

### 5.4 Framework Selection

- **Latest Frameworks**: Use recent frameworks that perform well, even if they are not as battle-tested.
- **Example**: MCP for social network interaction.

### 5.5 Development

- **Simplicity**: Keep components simple for easy implementation by AI agents.
- **Documentation**: Document all code, information flow, sources, and AI models.
- **Licensing**: Initially closed source and proprietary.

### 5.6 Iteration

- **Rapid Development**: Develop the first version quickly with minimal features.
- **Feedback Loop**: Short development cycles to allow for rapid iteration and feedback.

## 6. Additional Sections (if needed)

### 6.1 Security and Privacy

- **Data Protection**: Ensure all gathered data is protected and used ethically.
- **Compliance**: Adhere to relevant data protection regulations.

### 6.2 User Interface Design

- **Usability**: Design the UI to be intuitive and user-friendly.
- **Accessibility**: Ensure the website is accessible to all users.

### 6.3 Future Enhancements

- **Scalability**: Plan for future scalability to handle a larger set of entities.
- **Extensibility**: Design the system to be easily extendable to companies and governments.

---

This structured document should be clear and unambiguous, making it easier for an AI model to understand and implement. If you need further details or have specific questions, please let me know!

